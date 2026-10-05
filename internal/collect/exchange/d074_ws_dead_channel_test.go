// d074_ws_dead_channel_test.go —— D-074 验收锚：①冷却循环 ≥DeadRounds 轮→单条
// 「通道死（候选全零帧）」warn（DeadRewarn 去重、sawData 断链）；②冷却循环中的
// 重连不入 storm 滑窗（storm 告警静默、累计计数照常）；③双候选全零帧僵尸流端到端。
package exchange

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestD074_DeadChannelRoundsAndReset 通道死轮次语义（noteExit 单点）：
// <DeadRounds 轮零告警；达 3 轮恰 1 条；DeadRewarn 窗内不重复；
// sawData 连接收尾=通道活过证据 → 连续冷却轮断链清零。
func TestD074_DeadChannelRoundsAndReset(t *testing.T) {
	var mu sync.Mutex
	var warns []string
	spec := WSSpec{
		Name: "t:dead", CooldownKills: 2, CooldownWindow: time.Minute,
		DeadRounds: 3, DeadRewarn: 6 * time.Hour,
		AlertFn: func(level, msg string) {
			mu.Lock()
			defer mu.Unlock()
			warns = append(warns, level+":"+msg)
		},
	}
	st := &wsCandState{}
	st.cur.Store(-1)
	st.dialOrder(2)
	zombieKill := func() {
		st.cur.Store(0)
		st.dataKil.Store(true)
		st.sawData.Store(false)
		st.noteExit(spec)
		st.cur.Store(-1)
	}
	for i := 0; i < 5; i++ { // 2 轮冷却 + 1 孤杀 → 未达 3 轮
		zombieKill()
	}
	mu.Lock()
	n := len(warns)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("未达 3 轮即告警: %v", warns)
	}
	zombieKill() // 第 3 轮冷却 → 1 条
	zombieKill()
	zombieKill() // 第 4 轮——rewarn 6h 内不重复
	mu.Lock()
	n, first := len(warns), ""
	if n > 0 {
		first = warns[0]
	}
	mu.Unlock()
	if n != 1 {
		t.Fatalf("通道死应恰 1 条（3 轮触发+6h 去重）: %d %v", n, warns)
	}
	if !strings.Contains(first, "通道死（候选全零帧）") || !strings.HasPrefix(first, "warn:") {
		t.Fatalf("告警文案/级别不符: %q", first)
	}
	st.cur.Store(0)
	st.dataKil.Store(true)
	st.sawData.Store(true) // 到过数据帧的连接被杀 = 通道活过
	st.noteExit(spec)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.consecCool != 0 {
		t.Fatalf("sawData 连接未断链连续冷却轮: %d", st.consecCool)
	}
}

// TestD074_DeadRewarnWindow DeadRewarn 窗过后通道死再发（缩窗实证去重滚动）。
func TestD074_DeadRewarnWindow(t *testing.T) {
	var mu sync.Mutex
	var warns []string
	spec := WSSpec{
		Name: "t:rewarn", CooldownKills: 1, CooldownWindow: time.Minute,
		DeadRounds: 2, DeadRewarn: 5 * time.Millisecond,
		AlertFn: func(level, msg string) {
			mu.Lock()
			defer mu.Unlock()
			warns = append(warns, level+":"+msg)
		},
	}
	st := &wsCandState{}
	st.cur.Store(-1)
	st.dialOrder(2)
	zombieKill := func() {
		st.cur.Store(0)
		st.dataKil.Store(true)
		st.noteExit(spec)
		st.cur.Store(-1)
	}
	zombieKill() // 轮 1
	zombieKill() // 轮 2 → warn×1
	time.Sleep(6 * time.Millisecond)
	zombieKill() // 轮 3 → rewarn 窗过 → warn×2
	mu.Lock()
	defer mu.Unlock()
	if len(warns) != 2 {
		t.Fatalf("rewarn 滚动不符: %d %v", len(warns), warns)
	}
}

// TestD074_CooldownLoopActiveFlag 冷却循环标记：任一候选冷却中=true，过期=false。
func TestD074_CooldownLoopActiveFlag(t *testing.T) {
	st := &wsCandState{}
	st.cur.Store(-1)
	st.dialOrder(2)
	if st.cooldownLoopActive() {
		t.Fatal("无冷却不应在循环")
	}
	st.cooled[0] = time.Now().Add(-time.Second)
	if st.cooldownLoopActive() {
		t.Fatal("过期冷却不应在循环")
	}
	st.cooled[0] = time.Now().Add(time.Minute)
	if !st.cooldownLoopActive() {
		t.Fatal("冷却中应在循环")
	}
}

// TestD074_DeadChannelE2E 双候选全零帧僵尸流端到端（CooldownKills=1=每杀即冷却，
// 全程冷却循环中）：storm 告警静默（②经真 meter.record 链路）、「通道死」warn 在档（①）。
func TestD074_DeadChannelE2E(t *testing.T) {
	primURL, _ := wsRunnerServer(t, false, make(chan []byte, 8)) // pong 活/零数据帧=僵尸
	backURL, _ := wsRunnerServer(t, false, make(chan []byte, 8))
	var mu sync.Mutex
	var warns []string
	var reconns atomic.Int64
	meter := newReconnectMeter(primURL, func(level, msg string) {
		mu.Lock()
		defer mu.Unlock()
		warns = append(warns, level+":"+msg)
	})
	frames := make(chan []byte, 8)
	spec := wsRunnerSpec(primURL, frames)
	spec.Candidates = []WSCandidate{{URL: primURL}, {URL: backURL}}
	spec.ReadIdle = 5 * time.Second
	spec.PingEvery = 50 * time.Millisecond
	spec.DataIdle = 120 * time.Millisecond // >PingEvery，零帧僵尸窗必杀
	spec.ReconnectBackoff = 20 * time.Millisecond
	spec.CooldownKills = 1
	spec.CooldownWindow = 200 * time.Millisecond
	spec.DeadRounds = 3
	spec.DeadRewarn = 100 * time.Millisecond
	spec.AlertFn = meter.alertFn // D-074 ①：通道死与 storm 同告警管道（真 meter 旁路计数）
	spec.OnReconnect = func(inCooldownLoop bool) {
		meter.record(inCooldownLoop) // D-074 ②：真链路实证排除语义
		reconns.Add(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, spec)
	deadline := time.Now().Add(6 * time.Second)
	for reconns.Load() < 20 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if len(warns) < 2 {
		t.Fatalf("死通道未发通道死告警: reconns=%d warns=%v", reconns.Load(), warns)
	}
	for _, w := range warns {
		if strings.Contains(w, "[ws-storm]") {
			t.Fatalf("storm 告警泄漏（②未生效）: %q", warns)
		}
		if !strings.Contains(w, "通道死（候选全零帧）") {
			t.Fatalf("非预期告警: %q", w)
		}
	}
}
