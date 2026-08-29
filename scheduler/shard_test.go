package scheduler

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// TestNewPlan_Boundaries 断言各分片 [Start,End) 不重叠、无间隙、覆盖全文件（S-2），
// 并校验分片数决策：n = min(max(⌈size/8MiB⌉, 3), 6)。
func TestNewPlan_Boundaries(t *testing.T) {
	cases := []struct {
		name string
		size int64
		want int // 期望分片数
	}{
		{"small_1MiB", 1 << 20, 1},         // <5MiB 退化单连接
		{"small_4MiB", 4 << 20, 1},         // <5MiB
		{"exact_5MiB", 5 << 20, 3},         // ≥5MiB 进入多分片：max(⌈5/8⌉=1,3)=3
		{"mid_12MiB", 12 << 20, 3},         // ⌈12/8⌉=2 → 3，每片 4MiB ≤8MiB
		{"granularity_24MiB", 24 << 20, 3}, // 每片恰 8MiB = MaxShardSize
		{"granularity_25MiB", 25 << 20, 4}, // ⌈25/8⌉=4
		{"granularity_41MiB", 41 << 20, 6}, // ⌈41/8⌉=6 = MaxConnections
		{"capped_100MiB", 100 << 20, 6},    // 连接封顶 6
		{"capped_2GiB", 2 << 30, 6},        // H-2 内存红线场景仍封顶 6
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewPlan(c.size)
			if got := len(p.Shards); got != c.want {
				t.Fatalf("分片数=%d want %d", got, c.want)
			}
			assertContiguous(t, p.Shards, c.size)
		})
	}
}

// TestNewPlan_UnknownSize 大小未知时退化为单分片流式。
func TestNewPlan_UnknownSize(t *testing.T) {
	p := NewPlan(0)
	if len(p.Shards) != 1 || p.Shards[0].End != 0 {
		t.Fatalf("未知大小应返回单分片 End=0, got %+v", p.Shards)
	}
}

// TestNewPlanN_ExplicitCount 显式分片数（-n 参数）：边界收敛与覆盖不变量。
func TestNewPlanN_ExplicitCount(t *testing.T) {
	// 显式 6 片
	p := NewPlanN(10<<20, 6)
	if len(p.Shards) != 6 {
		t.Fatalf("显式 n=6 应得 6 片, got %d", len(p.Shards))
	}
	assertContiguous(t, p.Shards, 10<<20)
	// n=0 → 自动决策（3）
	if got := len(NewPlanN(12<<20, 0).Shards); got != 3 {
		t.Fatalf("n=0 应自动决策为 3, got %d", got)
	}
	// n 超上限收敛到显式上限 16（对齐 aria2 -x；自动决策仍封顶 6）
	if got := len(NewPlanN(30<<20, 99).Shards); got != MaxExplicitConnections {
		t.Fatalf("n=99 应收敛到 %d, got %d", MaxExplicitConnections, got)
	}
	if got := len(NewPlanN(30<<20, 16).Shards); got != 16 {
		t.Fatalf("n=16 应得 16 片, got %d", got)
	}
	// n 不超过字节数（避免零长分片）
	if got := len(NewPlanN(3, 6).Shards); got != 3 {
		t.Fatalf("size=3 时 n 应收敛到 3, got %d", got)
	}
}

// TestRebalance_SplitFastShard 快片吞吐 > 慢片 2 倍时应触发拆分，且拆分后地址连续。
func TestRebalance_SplitFastShard(t *testing.T) {
	p := NewPlan(12 << 20) // 3 片 × 4MiB
	before := len(p.Shards)
	// 让片0极快、片2极慢
	p.UpdateThroughput(0, 10<<20, time.Second) // 10 MiB/s
	p.UpdateThroughput(1, 3<<20, time.Second)  // 3 MiB/s
	p.UpdateThroughput(2, 1<<20, time.Second)  // 1 MiB/s (慢)

	if !p.Rebalance() {
		t.Fatal("应触发重平衡(快>慢2倍)")
	}
	if got := len(p.Shards); got != before+1 {
		t.Fatalf("应新增1片, before=%d after=%d", before, got)
	}
	// 重平衡后边界仍应覆盖全文件且无重叠（S-2 拆分回归断言）
	assertContiguous(t, p.Shards, 12<<20)
}

// TestRebalance_MergeStagnant 停滞片（吞吐=0 且未激活）合并到相邻片。
func TestRebalance_MergeStagnant(t *testing.T) {
	p := NewPlan(12 << 20) // 3 片
	p.Shards[1].Active = true
	p.Shards[1].throughput = 1 << 20 // 活跃片不受合并影响
	if !p.Rebalance() {
		t.Fatal("存在停滞片时应触发合并")
	}
	// 片0（无吞吐、非活跃）并入片1：分片数 3→2
	if got := len(p.Shards); got != 2 {
		t.Fatalf("合并后应剩 2 片, got %d", got)
	}
	assertContiguous(t, p.Shards, 12<<20)
}

// assertContiguous 断言分片地址连续且全覆盖 [0,size)。
func assertContiguous(t *testing.T, shards []Shard, size int64) {
	t.Helper()
	var covered int64
	prev := int64(0)
	for _, s := range shards {
		if s.Start != prev {
			t.Fatalf("间隙: prevEnd=%d nextStart=%d（分片不连续）", prev, s.Start)
		}
		if s.End <= s.Start {
			t.Fatalf("非法区间 [%d,%d)", s.Start, s.End)
		}
		covered += s.End - s.Start
		prev = s.End
	}
	if covered != size {
		t.Fatalf("覆盖 %d want %d", covered, size)
	}
}

// TestPriorityQueue_Order 最大堆：优先级高者先出。
func TestPriorityQueue_Order(t *testing.T) {
	s := NewScheduler(4)
	s.Submit(&Task{ID: "low", Priority: 1})
	s.Submit(&Task{ID: "high", Priority: 10})
	s.Submit(&Task{ID: "mid", Priority: 5})

	order := []string{}
	for {
		task, err := s.Next(context.Background())
		if err != nil {
			break
		}
		order = append(order, task.ID)
		s.Done(task.ID)
	}
	want := []string{"high", "mid", "low"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("出队顺序=%v want %v", order, want)
	}
}

// TestScheduler_CPUModeSwitch R-3 模式开关。
func TestScheduler_CPUModeSwitch(t *testing.T) {
	s := NewScheduler(4)
	if s.cpuLimit != 0.6 {
		t.Fatalf("默认应为 60%% 限速")
	}
	s.SetMode(ModeMaxPerf)
	if s.cpuLimit != 1.0 {
		t.Fatalf("最大性能模式应为 1.0, got %v", s.cpuLimit)
	}
	s.SetMode(ModeDefault)
	if s.cpuLimit != 0.6 {
		t.Fatalf("切回默认应为 0.6")
	}
}
