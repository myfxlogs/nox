package deribit

import (
	"fmt"
	"sync"
	"time"
)

// AlertFn 告警插入回调（适配 store.InsertAlert 签名差 ctx，main 注入）。
type AlertFn func(level, msg string)

// alertState 告警状态机的 per-instrument 状态。
type alertState struct {
	consecutive int       // 连续超阈 tick 计数
	lastAlert   time.Time // 上次告警时间（去重 ≥1h）
}

// AlertRule 平价偏离告警规则（D-137 §1）。
// |dev_bps| > Threshold 连续 ≥MinConsecutive tick → 插 1 条 info 告警；
// 同 instrument 告警间隔 ≥RealertInterval（防刷）；恢复不告警（第一版不做 recovered）。
type AlertRule struct {
	Threshold       float64          // |dev_bps| 阈值（默认 15）
	MinConsecutive  int              // 连续超阈 tick 数（默认 3）
	RealertInterval time.Duration    // 同 instrument 告警间隔（默认 1h）
	Now             func() time.Time // 可注入时钟（测试用），nil→time.Now
	Alert           AlertFn          // 告警回调（main 注入 store.InsertAlert）

	mu     sync.Mutex
	states map[string]alertState // instrument canonical → 状态
}

// NewAlertRule 创建告警规则（默认值）。
func NewAlertRule(threshold float64, alertFn AlertFn) *AlertRule {
	return &AlertRule{
		Threshold:       threshold,
		MinConsecutive:  3,
		RealertInterval: time.Hour,
		Now:             time.Now,
		Alert:           alertFn,
		states:          make(map[string]alertState),
	}
}

// nowTime 返回当前时钟（可注入）。
func (r *AlertRule) nowTime() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Evaluate 评估一批 ParityResult，更新状态机，触发告警。
// 每 tick 对所有 result 调用：超阈→consecutive++；未超阈→consecutive=0。
// 达到 MinConsecutive 且距上次告警 ≥RealertInterval → 插告警。
func (r *AlertRule) Evaluate(results []ParityResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.nowTime()
	// 本 tick 超阈的 instrument 集合（用于重置未超阈的 consecutive）
	overThreshold := make(map[string]bool, len(results))
	for _, res := range results {
		if absBps(res.DevBps) > r.Threshold {
			overThreshold[res.Instrument.Canonical] = true
		}
	}
	// 更新所有已知 instrument 状态（未超阈→重置）
	for inst := range r.states {
		if !overThreshold[inst] {
			s := r.states[inst]
			s.consecutive = 0
			r.states[inst] = s
		}
	}
	// 处理超阈的
	for _, res := range results {
		inst := res.Instrument.Canonical
		if !overThreshold[inst] {
			continue
		}
		s := r.states[inst]
		s.consecutive++
		if s.consecutive >= r.MinConsecutive {
			// 检查去重间隔
			if !s.lastAlert.IsZero() && now.Sub(s.lastAlert) < r.RealertInterval {
				r.states[inst] = s
				continue
			}
			// 触发告警
			if r.Alert != nil {
				msg := fmt.Sprintf("opt-parity %s dev=%.2fbps |C=%.2f P=%.2f F=%.2f K=%.2f|",
					inst, res.DevBps, res.C, res.P, res.F, res.K)
				r.Alert("info", msg)
			}
			s.lastAlert = now
			s.consecutive = 0 // 告警后重置，下一轮重新计连续
		}
		r.states[inst] = s
	}
}

// Reset 清空状态（测试用）。
func (r *AlertRule) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = make(map[string]alertState)
}
