package updater

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Updater struct {
	AppName     string // tgfulibot
	AppDir      string // /opt/tgfulibot
	BinaryPath  string // /opt/tgfulibot/backend/tgfulibot
	ServiceName string // tgfulibot.service
	VersionFile string // /opt/tgfulibot/backend/VERSION（升级成功后写入新版本号）
}

// Update 下载并应用新版本，返回成功后的版本号
func (u *Updater) Update(version, downloadURL string) error {
	if version == "" || downloadURL == "" {
		return fmt.Errorf("version/downloadURL 不能为空")
	}

	tmpDir, err := os.MkdirTemp("", "bushubot-upd-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. 下载 tar.gz + sha256
	tgzPath := filepath.Join(tmpDir, "release.tar.gz")
	if err := download(downloadURL, tgzPath); err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	// 校验 sha256（CI 一定生成 .sha256；下载失败/校验失败都拒绝）
	if err := verifySHA256(tgzPath, downloadURL+".sha256"); err != nil {
		return fmt.Errorf("sha256 校验失败: %w", err)
	}

	// 2. 解压到临时目录
	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return err
	}
	if err := extractTarGz(tgzPath, extractDir); err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}

	// 3. 找出解压后的目录（应该只有一个）
	entries, _ := os.ReadDir(extractDir)
	var stage string
	for _, e := range entries {
		if e.IsDir() {
			stage = filepath.Join(extractDir, e.Name())
			break
		}
	}
	if stage == "" {
		return fmt.Errorf("解压结果异常：找不到产物目录")
	}

	// 4. 备份旧二进制
	timestamp := time.Now().Format("20060102150405")
	bakPath := u.BinaryPath + ".bak." + timestamp
	if _, err := os.Stat(u.BinaryPath); err == nil {
		if err := os.Rename(u.BinaryPath, bakPath); err != nil {
			return fmt.Errorf("备份旧二进制失败: %w", err)
		}
	}

	// 5. 替换二进制
	newBin := filepath.Join(stage, u.AppName)
	if err := copyFile(newBin, u.BinaryPath, 0o755); err != nil {
		// 失败时尝试还原
		_ = os.Rename(bakPath, u.BinaryPath)
		return fmt.Errorf("替换二进制失败: %w", err)
	}
	// 恢复属主（systemd 服务通常以 tgfulibot 用户运行）
	_ = exec.Command("chown", u.AppName+":"+u.AppName, u.BinaryPath).Run()

	// 6. 替换 migrations 目录（项目当前从文件系统读）
	//    备份策略：把旧目录改名为 .bak.<timestamp>，复制新的；回滚时反向操作
	srcMig := filepath.Join(stage, "migrations")
	dstMig := filepath.Join(u.AppDir, "backend", "migrations")
	migBakPath := dstMig + ".bak." + timestamp
	migReplaced := false
	if _, err := os.Stat(srcMig); err == nil {
		// 先把旧 migrations 改名（不删，以备回滚）
		if _, err := os.Stat(dstMig); err == nil {
			if err := os.Rename(dstMig, migBakPath); err != nil {
				return fmt.Errorf("备份旧 migrations 失败: %w", err)
			}
		}
		// 复制新的
		if err := exec.Command("cp", "-r", srcMig, dstMig).Run(); err != nil {
			// 复制失败，把旧 migrations 还原
			_ = os.Rename(migBakPath, dstMig)
			return fmt.Errorf("复制新 migrations 失败: %w", err)
		}
		_ = exec.Command("chown", "-R", u.AppName+":"+u.AppName, dstMig).Run()
		migReplaced = true
	}

	// 7. 重启服务（失败时回滚到旧二进制 + 旧 migrations 并再次重启）
	if err := u.RestartService(); err != nil {
		restartErr := err
		// 回滚 migrations
		if migReplaced {
			_ = os.RemoveAll(dstMig)
			_ = os.Rename(migBakPath, dstMig)
		}
		// 回滚二进制
		_ = os.Remove(u.BinaryPath)
		if rbErr := os.Rename(bakPath, u.BinaryPath); rbErr != nil {
			return fmt.Errorf("重启失败 (%v) 且回滚失败 (%v) — 服务可能已停摆", restartErr, rbErr)
		}
		if rbRestartErr := u.RestartService(); rbRestartErr != nil {
			return fmt.Errorf("重启失败 (%v) 已回滚旧版本但仍无法启动 (%v)", restartErr, rbRestartErr)
		}
		return fmt.Errorf("新版本重启失败 (%v) 已自动回滚到旧版本（含 migrations）", restartErr)
	}

	// 8. 写入新版本号到 VERSION 文件（关键！防止下次心跳读到旧版本反复触发升级）
	if u.VersionFile != "" {
		if err := os.WriteFile(u.VersionFile, []byte(version+"\n"), 0o644); err != nil {
			// 写失败不影响本次升级，但下次心跳会再触发；记日志让上层警觉
			return fmt.Errorf("升级成功但写 VERSION 文件失败 (%w)，下次心跳可能重复触发升级", err)
		}
		_ = exec.Command("chown", u.AppName+":"+u.AppName, u.VersionFile).Run()
	}

	// 9. 清理过期备份（保留最近 5 份二进制 + 5 份 migrations）
	cleanupBackups(filepath.Dir(u.BinaryPath), filepath.Base(u.BinaryPath)+".bak.", 5)
	cleanupBackups(filepath.Dir(dstMig), filepath.Base(dstMig)+".bak.", 5)

	return nil
}

func (u *Updater) RestartService() error {
	cmd := exec.Command("systemctl", "restart", u.ServiceName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, string(out))
	}
	return nil
}

func (u *Updater) StopService() error {
	return exec.Command("systemctl", "stop", u.ServiceName).Run()
}

// SelfUpdate 用于 agent 自我更新自己的二进制。
// 成功后调用方应当 os.Exit(0)，由 systemd Restart=always 用新二进制拉起来。
//
// 必须传入：
//   - version: 目标版本（仅日志用）
//   - downloadURL: agent tar.gz 下载地址
//   - agentBinaryName: tar.gz 内的二进制名（例如 "bushubot-agent"）
//   - selfBinaryPath: 当前 agent 二进制的绝对路径
func (u *Updater) SelfUpdate(version, downloadURL, agentBinaryName, selfBinaryPath string) error {
	if version == "" || downloadURL == "" {
		return fmt.Errorf("version/downloadURL 不能为空")
	}

	tmpDir, err := os.MkdirTemp("", "bushubot-self-upd-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	tgzPath := filepath.Join(tmpDir, "agent.tar.gz")
	if err := download(downloadURL, tgzPath); err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	if err := verifySHA256(tgzPath, downloadURL+".sha256"); err != nil {
		return fmt.Errorf("sha256 校验失败: %w", err)
	}

	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return err
	}
	if err := extractTarGz(tgzPath, extractDir); err != nil {
		return fmt.Errorf("解压失败: %w", err)
	}

	// 找到解压目录里的新 agent 二进制
	var newBin string
	_ = filepath.Walk(extractDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Base(p) == agentBinaryName {
			newBin = p
		}
		return nil
	})
	if newBin == "" {
		return fmt.Errorf("解压结果里找不到 %s", agentBinaryName)
	}

	// 备份当前 agent 二进制（万一新版起不来，systemd 也认旧路径，用 .failed 标记便于排查）
	timestamp := time.Now().Format("20060102150405")
	bakPath := selfBinaryPath + ".bak." + timestamp
	if err := copyFile(selfBinaryPath, bakPath, 0o755); err != nil {
		return fmt.Errorf("备份当前 agent 失败: %w", err)
	}

	// 替换自身二进制（Linux 允许覆盖运行中的二进制）
	if err := copyFile(newBin, selfBinaryPath, 0o755); err != nil {
		return fmt.Errorf("替换 agent 二进制失败: %w", err)
	}

	// 写一个标记文件，避免重复升到同一版本
	_ = os.WriteFile(filepath.Join(filepath.Dir(selfBinaryPath), ".last-self-update"),
		[]byte(version+"\n"), 0o644)

	cleanupBackups(filepath.Dir(selfBinaryPath), filepath.Base(selfBinaryPath)+".bak.", 3)
	return nil
}

// ---------------- helpers ----------------

// 下载用的 HTTP client：连接 30s 超时，整体最多 5 分钟（解压前用，比较富余）
// GitHub 偶尔很慢，但不能让 agent 主循环卡死
var downloadHTTPClient = &http.Client{
	Timeout: 5 * time.Minute,
}

// 小文件 (sha256) 用更短的超时
var smallFileHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

// verifySHA256 下载 .sha256 文件，校验本地 tgz 的哈希
// CI 出包时会同时生成 <stage>.tar.gz 和 <stage>.tar.gz.sha256
func verifySHA256(localPath, remoteSHAURL string) error {
	resp, err := smallFileHTTPClient.Get(remoteSHAURL)
	if err != nil {
		return fmt.Errorf("下载 sha256 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("sha256 文件 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// sha256sum 输出格式: "<hex>  <filename>"，只取第一段
	expected := strings.Fields(strings.TrimSpace(string(body)))
	if len(expected) == 0 {
		return fmt.Errorf("sha256 文件内容为空")
	}
	expectedHex := strings.ToLower(expected[0])

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return err
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != expectedHex {
		return fmt.Errorf("sha256 不匹配: 期望 %s, 实际 %s", expectedHex, got)
	}
	return nil
}

func download(url, dst string) error {
	resp, err := downloadHTTPClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// 防 zip slip
		target := filepath.Join(dst, h.Name)
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("非法路径: %s", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, os.FileMode(h.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func cleanupBackups(dir, prefix string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		name string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{e.Name(), info.ModTime()})
	}
	if len(items) <= keep {
		return
	}
	// 按时间倒序，删除靠后的
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].mod.After(items[i].mod) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	for _, it := range items[keep:] {
		// 用 RemoveAll 同时支持文件（旧二进制备份）和目录（migrations 备份）
		_ = os.RemoveAll(filepath.Join(dir, it.name))
	}
}
