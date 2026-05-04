package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// 基本比较
		{"v0.10", "v0.2", 1}, // 关键 case：v0.10 > v0.2
		{"v0.2", "v0.10", -1},
		{"v0.13", "v0.13", 0},
		{"agent-v0.10", "agent-v0.2", 1},
		{"agent-v0.10", "agent-v0.10", 0},
		{"agent-v1.0", "agent-v0.99", 1},
		{"agent-v1.2.3", "agent-v1.2.4", -1},
		{"agent-v1.2.3", "agent-v1.2.3", 0},
		{"", "v0.1", -1},
		{"v0.1", "", 1},
		{"agent-v0.10.0", "agent-v0.10", 0}, // 末尾 0 等价

		// 用户的版本规则: 整十用两位 (v0.40/v0.50)，中间正常 (v0.41/v0.42...)
		{"v0.41", "v0.40", 1},
		{"v0.49", "v0.40", 1},
		{"v0.50", "v0.49", 1}, // 整十跳变
		{"v0.99", "v0.50", 1},
		{"v1.00", "v0.99", 1},  // 跨大版本
		{"v1.10", "v1.00", 1},  // 大版本整十
		{"v1.20", "v1.10", 1},
		{"v1.10", "v1.09", 1},  // 10 > 9（语义比较保证）
		{"v2.00", "v1.99", 1},
		{"v0.49", "v0.50", -1}, // 防降级：管理员手滑设旧版本不会触发更新

		// 自比较
		{"v1.10", "v1.10", 0},
		{"v0.40", "v0.40", 0},
		// 注意：你的规则下不会写 v1.1（要么 v1.10 整十，要么 v1.01/v1.09/v1.11+）
		// 但万一手滑写了 v1.1，按语义 1 < 10，所以 v1.10 > v1.1，不会被误判为相等
		{"v1.10", "v1.1", 1},
	}
	for _, c := range cases {
		got := Compare(c.a, c.b)
		if got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestLessThan(t *testing.T) {
	if !LessThan("v0.2", "v0.10") {
		t.Fatal("v0.2 应该 < v0.10")
	}
	if LessThan("v0.10", "v0.2") {
		t.Fatal("v0.10 不应该 < v0.2")
	}
}
