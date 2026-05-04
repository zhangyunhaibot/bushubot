// Package sysinfo 用纯 Go 标准库 + Linux /proc 采集系统指标。
// 不引入 gopsutil 等外部依赖，编译产物保持小。
package sysinfo

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Snapshot struct {
	MemUsedMB     int     // 已用内存 MB
	MemTotalMB    int     // 总内存 MB
	DiskUsedGB    int     // / 分区已用 GB
	DiskTotalGB   int     // / 分区总 GB
	Load1m        float64 // 1 分钟 load average
	CPUCount      int     // CPU 核心数
	UptimeSeconds int64   // 系统已运行秒数
}

func Collect() Snapshot {
	s := Snapshot{CPUCount: runtime.NumCPU()}
	if mu, mt, ok := readMemory(); ok {
		s.MemUsedMB = mu
		s.MemTotalMB = mt
	}
	if du, dt, ok := readDisk("/"); ok {
		s.DiskUsedGB = du
		s.DiskTotalGB = dt
	}
	if l1, ok := readLoadAvg(); ok {
		s.Load1m = l1
	}
	if up, ok := readUptime(); ok {
		s.UptimeSeconds = up
	}
	return s
}

// readMemory 从 /proc/meminfo 算已用内存
// 已用 = MemTotal - MemAvailable（更贴近"用户感知的可用内存"）
func readMemory() (usedMB, totalMB int, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	var memTotalKB, memAvailKB int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			memTotalKB = parseKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			memAvailKB = parseKB(line)
		}
		if memTotalKB > 0 && memAvailKB > 0 {
			break
		}
	}
	if memTotalKB == 0 {
		return 0, 0, false
	}
	totalMB = memTotalKB / 1024
	usedMB = (memTotalKB - memAvailKB) / 1024
	return usedMB, totalMB, true
}

// readDisk 用 statfs 系统调用读 path 所在分区的容量
func readDisk(path string) (usedGB, totalGB int, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	totalBytes := st.Blocks * uint64(st.Bsize)
	availBytes := st.Bavail * uint64(st.Bsize)
	usedBytes := totalBytes - availBytes
	totalGB = int(totalBytes / (1024 * 1024 * 1024))
	usedGB = int(usedBytes / (1024 * 1024 * 1024))
	return usedGB, totalGB, true
}

// readLoadAvg 读 /proc/loadavg
func readLoadAvg() (load1m float64, ok bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	parts := strings.Fields(string(data))
	if len(parts) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readUptime 读 /proc/uptime（系统启动以来秒数）
func readUptime() (int64, bool) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	parts := strings.Fields(string(data))
	if len(parts) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, false
	}
	return int64(v), true
}

func parseKB(line string) int {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(fields[1])
	return n
}

// FormatUptime 把秒数格式化成 "3d 4h 12m"
func FormatUptime(secs int64) string {
	if secs <= 0 {
		return "?"
	}
	d := secs / 86400
	h := (secs % 86400) / 3600
	m := (secs % 3600) / 60
	if d > 0 {
		return strconv.FormatInt(d, 10) + "d " +
			strconv.FormatInt(h, 10) + "h " +
			strconv.FormatInt(m, 10) + "m"
	}
	if h > 0 {
		return strconv.FormatInt(h, 10) + "h " + strconv.FormatInt(m, 10) + "m"
	}
	return strconv.FormatInt(m, 10) + "m"
}

// FetchServiceLogs 抓 systemd 服务最近 lines 行日志（用 journalctl）
// 返回截断到 maxBytes 字节内的内容（保留尾部）
func FetchServiceLogs(serviceName string, lines int, maxBytes int, timeout time.Duration) (string, error) {
	out, err := runCommand(timeout, "journalctl", "-u", serviceName,
		"-n", strconv.Itoa(lines), "--no-pager", "--output", "short")
	if maxBytes > 0 && len(out) > maxBytes {
		out = "...(已截断头部)\n" + out[len(out)-maxBytes:]
	}
	return out, err
}
