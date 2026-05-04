package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"bushubot-agent/internal/bot"
	"bushubot-agent/internal/client"
	"bushubot-agent/internal/config"
	"bushubot-agent/internal/sysinfo"
	"bushubot-agent/internal/updater"
	"bushubot-agent/internal/version"
)

// Version 由 ldflags 注入
var Version = "dev"

func main() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("agent panic: %v\n%s", r, debug.Stack())
			os.Exit(1) // systemd Restart=always 会拉起
		}
	}()

	cfgPath := flag.String("config", "/opt/tgfulibot/agent/config.json", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if cfg.VersionFile != "" {
		versionFilePath = cfg.VersionFile
	}
	log.Printf("bushubot-agent %s 启动 (master=%s)", Version, cfg.MasterURL)

	upd := &updater.Updater{
		AppName:     cfg.AppName,
		AppDir:      cfg.AppDir,
		BinaryPath:  cfg.BinaryPath,
		ServiceName: cfg.ServiceName,
		VersionFile: cfg.VersionFile, // 升级后写新版本，防止反复触发升级
	}

	masterCli := client.New(cfg.MasterURL, cfg.LicenseKey)

	state := &state{}

	// 客户 Bot 仅用于推送通知（不接命令、不长轮询，避免跟 tgfulibot 抢 getUpdates）
	botHandler, err := bot.New(cfg.BotToken, cfg.OwnerTGID)
	if err != nil {
		log.Fatalf("客户 Bot 初始化失败: %v", err)
	}

	// 两个独立 ticker:
	//   - heartbeatTick: 健康心跳, 上报 cpu/内存/磁盘 指标 (默认 20 min)
	//   - commandTick:   命令拉取, 拉 master 推送的远程指令 (默认 60s)
	// 拆开是因为心跳放慢后 (20 min) 远程操作 ≤60s 生效不能等心跳, 必须独立拉
	heartbeatTick := time.NewTicker(time.Duration(cfg.HeartbeatIntervalSeconds) * time.Second)
	defer heartbeatTick.Stop()
	commandTick := time.NewTicker(time.Duration(cfg.CommandPullIntervalSeconds) * time.Second)
	defer commandTick.Stop()

	graceDuration := time.Duration(cfg.GraceDaysOffline) * 24 * time.Hour

	// 立即触发一次首次心跳, 让 master 知道客户上线了, 否则要等 20 min
	go func() {
		if hb, err := heartbeat(masterCli, state); err != nil {
			log.Printf("首次心跳失败: %v", err)
			handleHeartbeatFailure(upd, state, graceDuration)
		} else {
			state.markSuccess()
			handleHeartbeatResp(upd, masterCli, state, hb)
		}
	}()

	// 信号处理
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-heartbeatTick.C:
			hb, err := heartbeat(masterCli, state)
			if err != nil {
				log.Printf("心跳失败: %v", err)
				handleHeartbeatFailure(upd, state, graceDuration)
				continue
			}
			state.markSuccess()
			handleHeartbeatResp(upd, masterCli, state, hb)
		case <-commandTick.C:
			pullNotifications(masterCli, botHandler, upd, state)
		case <-sig:
			log.Println("收到退出信号，停止 agent")
			return
		}
	}
}

// handleHeartbeatFailure 心跳失败时的处理：
// - 短期失败（< grace）只记录日志，不影响 tgfulibot
// - 长期失败（>= grace）才停 tgfulibot，防止恶意客户故意切网长期使用
func handleHeartbeatFailure(upd *updater.Updater, st *state, grace time.Duration) {
	last := st.lastSuccessAt()
	if last.IsZero() {
		// 从未成功过，可能是首次启动 master 不可达，先跑着
		return
	}
	since := time.Since(last)
	if since < grace {
		return
	}
	log.Printf("已离线超过 %s（最后成功心跳 %s 前），停止 tgfulibot", grace, since)
	_ = upd.StopService()
}

// ---------------- 内部状态 ----------------

type state struct {
	mu              sync.RWMutex
	currentVersion  string
	latestVersion   string
	lastHeartbeat   string
	lastSuccessTime time.Time
}

type stateSnapshot struct {
	currentVersion string
	latestVersion  string
}

func (s *state) read() stateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return stateSnapshot{s.currentVersion, s.latestVersion}
}

func (s *state) update(fn func(*state)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *state) markSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSuccessTime = time.Now()
}

func (s *state) lastSuccessAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSuccessTime
}

// ---------------- 业务函数 ----------------

func heartbeat(cli *client.Client, st *state) (*client.HeartbeatResponse, error) {
	hostname, _ := os.Hostname()
	ip := localIP()
	cv := readCurrentVersion()
	si := sysinfo.Collect()

	resp, err := cli.Heartbeat(client.HeartbeatRequest{
		CurrentVersion: cv,
		AgentVersion:   Version,
		Hostname:       hostname,
		ServerIP:       ip,
		MemUsedMB:      si.MemUsedMB,
		MemTotalMB:     si.MemTotalMB,
		DiskUsedGB:     si.DiskUsedGB,
		DiskTotalGB:    si.DiskTotalGB,
		Load1m:         si.Load1m,
		CPUCount:       si.CPUCount,
		UptimeSeconds:  si.UptimeSeconds,
	})
	if err != nil {
		return nil, err
	}

	st.update(func(s *state) {
		s.currentVersion = cv
		s.latestVersion = resp.LatestVersion
		s.lastHeartbeat = time.Now().Format("2006-01-02 15:04:05")
	})
	return resp, nil
}

func handleHeartbeatResp(upd *updater.Updater, cli *client.Client, st *state, resp *client.HeartbeatResponse) {
	// 1. 服务被远程停用
	if !resp.Enabled {
		log.Println("master 标记客户为 disabled，停止 tgfulibot 服务")
		_ = upd.StopService()
		return
	}

	// 2. agent 自我更新优先于业务更新
	//    （强制版本检查 + 新版本检查）
	if shouldSelfUpdate(resp) {
		log.Printf("发现新 agent 版本 %s，开始自我更新", resp.LatestAgentVersion)
		if err := selfUpdateAndExit(upd, cli, resp.LatestAgentVersion, resp.AgentDownloadURL); err != nil {
			log.Printf("自我更新失败: %v", err)
		}
		// 自我更新成功不会执行到这里（已 os.Exit）
		return
	}

	// 3. 有新版本才更新 tgfulibot（语义比较，防降级 + 防字面歧义如 v1.01 / v1.1）
	if resp.LatestVersion == "" {
		return
	}
	if version.Compare(resp.LatestVersion, st.read().currentVersion) <= 0 {
		return
	}
	log.Printf("发现新版本 %s，开始更新", resp.LatestVersion)
	if err := performUpdate(upd, cli, st, resp.LatestVersion, resp.DownloadURL); err != nil {
		log.Printf("更新失败: %v", err)
	}
}

// shouldSelfUpdate 判断 agent 自身是否需要升级
//
// 关键约束（语义版本比较，不是字面）:
//   - 只在 latest > current 时升级（防止管理员手滑设了旧版本导致降级）
//   - latest == current（语义上相等，比如 v1.01 / v1.1）→ 不升级
//   - min_supported_agent_version > current → 强制升级（即使 latest 不大于 current）
func shouldSelfUpdate(resp *client.HeartbeatResponse) bool {
	if resp.AgentDownloadURL == "" {
		return false
	}
	// 不重复升到同一目标版本（防 master 配置错误导致循环）
	if last, _ := os.ReadFile(selfUpdateMarkerPath()); strings.TrimSpace(string(last)) == resp.LatestAgentVersion {
		return false
	}
	// 强制版本：当前版本 < min_supported_agent_version 必须升
	if resp.MinSupportedAgentVersion != "" && version.LessThan(Version, resp.MinSupportedAgentVersion) {
		return true
	}
	// 普通升级：只在 latest > current 时才升（语义比较，防降级 + 防字面歧义）
	if resp.LatestAgentVersion != "" && version.Compare(resp.LatestAgentVersion, Version) > 0 {
		return true
	}
	return false
}

func selfUpdateAndExit(upd *updater.Updater, cli *client.Client, version, url string) error {
	_ = cli.Report(client.ReportRequest{EventType: "agent_self_update_started", Version: version})

	selfPath, err := os.Executable()
	if err != nil {
		return err
	}
	if err := upd.SelfUpdate(version, url, "bushubot-agent", selfPath); err != nil {
		_ = cli.Report(client.ReportRequest{
			EventType: "agent_self_update_failed", Version: version, ErrorMessage: err.Error(),
		})
		return err
	}
	_ = cli.Report(client.ReportRequest{EventType: "agent_self_update_done", Version: version})

	log.Printf("agent 自我更新到 %s 完成，主动退出由 systemd 拉起新二进制", version)
	os.Exit(0)
	return nil // unreachable
}

func selfUpdateMarkerPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "/tmp/.bushubot-agent-last-update"
	}
	return filepath.Join(filepath.Dir(exe), ".last-self-update")
}

func performUpdate(upd *updater.Updater, cli *client.Client, st *state, version, url string) error {
	_ = cli.Report(client.ReportRequest{EventType: "update_started", Version: version})

	if err := upd.Update(version, url); err != nil {
		_ = cli.Report(client.ReportRequest{
			EventType:    "update_failed",
			Version:      version,
			ErrorMessage: err.Error(),
		})
		return err
	}

	st.update(func(s *state) { s.currentVersion = version })
	_ = cli.Report(client.ReportRequest{EventType: "update_done", Version: version})
	log.Printf("更新到 %s 成功", version)
	return nil
}

// 通知 type 枚举（必须跟 master/internal/store 保持一致）
const (
	notifTypeManual         = "manual"
	notifTypeRestartService = "restart_service"
	notifTypeForceUpdate    = "force_update"
	notifTypeUpdateInfo     = "update_info" // 文本形式的更新通知（"已更新到 vX"）
	notifTypeFetchLogs      = "fetch_logs"  // 让 agent 抓本地日志上传
)

func pullNotifications(cli *client.Client, bh *bot.Handler, upd *updater.Updater, st *state) {
	resp, err := cli.Notifications()
	if err != nil {
		log.Printf("拉通知失败: %v", err)
		return
	}
	for _, n := range resp.Notifications {
		ok := handleOneNotification(cli, bh, upd, st, n)
		if ok {
			// 执行成功才 ack；失败让 master 兜底（attempts ≥ 3 强制 deliver + alerter 推告警）
			if err := cli.AckNotification(n.ID); err != nil {
				log.Printf("ack 通知 #%d 失败: %v", n.ID, err)
			}
		}
	}
}

// handleOneNotification 返回 true 表示执行成功（agent 应当 ack）
func handleOneNotification(cli *client.Client, bh *bot.Handler, upd *updater.Updater, st *state, n client.Notification) bool {
	switch n.Type {
	case notifTypeRestartService:
		log.Printf("收到 master 指令: 重启 tgfulibot")
		if err := upd.RestartService(); err != nil {
			_ = cli.Report(client.ReportRequest{EventType: "restart_failed", ErrorMessage: err.Error()})
			log.Printf("重启失败: %v", err)
			return false
		}
		_ = cli.Report(client.ReportRequest{EventType: "restart_done"})
		_ = bh.SendNotification("🔄 服务已根据管理员指令重启")
		return true

	case notifTypeForceUpdate:
		log.Printf("收到 master 指令: 立即检查更新")
		hb, err := heartbeat(cli, st)
		if err != nil {
			log.Printf("强制更新心跳失败: %v", err)
			return false
		}
		st.markSuccess()
		handleHeartbeatResp(upd, cli, st, hb)
		return true

	case notifTypeFetchLogs:
		// message 格式约定: "<service>|<lines>"，省略 lines 时默认 100
		service, lines := parseFetchLogsArg(n.Message)
		log.Printf("收到 master 指令: 拉取 %s 日志（%d 行）", service, lines)
		content, err := sysinfo.FetchServiceLogs(service, lines, 50000, 15*time.Second)
		if err != nil {
			log.Printf("抓日志失败（继续上传错误信息）: %v", err)
			content = "[抓日志命令失败] " + err.Error() + "\n\n" + content
		}
		if len(content) > 50000 {
			content = content[len(content)-50000:]
			content = "...(已截断头部)\n" + content
		}
		if err := cli.PostLogs(client.LogsRequest{
			Service: service, Content: content, Bytes: len(content),
		}); err != nil {
			log.Printf("上传日志失败: %v", err)
			return false
		}
		return true

	case notifTypeManual, notifTypeUpdateInfo, "":
		fallthrough
	default:
		if n.Message != "" {
			if err := bh.SendNotification(n.Message); err != nil {
				log.Printf("推送通知给客户失败: %v", err)
				return false
			}
		}
		return true
	}
}

// ---------------- helpers ----------------

// versionFilePath 由 main 注入（避免硬编码 /opt/tgfulibot/backend/VERSION）
var versionFilePath = "/opt/tgfulibot/backend/VERSION"

func readCurrentVersion() string {
	data, err := os.ReadFile(versionFilePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func localIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// parseFetchLogsArg 解析 "<service>|<lines>" 格式
func parseFetchLogsArg(msg string) (service string, lines int) {
	service = "tgfulibot.service"
	lines = 100
	if msg == "" {
		return
	}
	parts := strings.SplitN(msg, "|", 2)
	if parts[0] != "" {
		service = parts[0]
	}
	if len(parts) == 2 {
		if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && n > 0 && n <= 5000 {
			lines = n
		}
	}
	return
}
