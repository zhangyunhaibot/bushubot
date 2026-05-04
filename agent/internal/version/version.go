// Package version 实现轻量语义版本比较。
//
// 支持的格式（前缀可选）：
//   v0.13       agent-v0.10       agent-v1.2.3
//
// 比较时只看数字部分（按 . 分割逐段做整数比较），前缀任意。
// 这样 "agent-v0.10" > "agent-v0.2"（按数字算 10 > 2）。
package version

import (
	"strconv"
	"strings"
)

// Compare 比较 a 和 b 的语义版本。
//
//	a < b → -1
//	a == b → 0
//	a > b → 1
//
// 解析失败的版本被当作 "0"，永远落后。
func Compare(a, b string) int {
	pa := parse(a)
	pb := parse(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		va, vb := 0, 0
		if i < len(pa) {
			va = pa[i]
		}
		if i < len(pb) {
			vb = pb[i]
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}

// LessThan 等价于 Compare(a, b) < 0
func LessThan(a, b string) bool { return Compare(a, b) < 0 }

// parse 把 "agent-v0.10.3" 解析成 [0, 10, 3]
func parse(s string) []int {
	// 找到第一个数字开始的位置
	i := 0
	for i < len(s) && !isDigit(s[i]) {
		i++
	}
	s = s[i:]
	if s == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ".") {
		// 清理可能的非数字尾部（如 "3-rc1"）
		end := 0
		for end < len(part) && isDigit(part[end]) {
			end++
		}
		n, err := strconv.Atoi(part[:end])
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
