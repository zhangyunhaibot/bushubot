package sysinfo

import (
	"context"
	"os/exec"
	"time"
)

// runCommand 跑一个命令，超时则杀掉，返回（截断的）合并 stdout+stderr
func runCommand(timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}
