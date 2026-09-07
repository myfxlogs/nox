// D-252 CMC 净值阈值告警状态机（转换式+2 tick 去抖，禁单样本触发）。
// equity ≤ CloseoutLevelUsd → closeout 区；≤ WarningLevelUsd → warning 区；否则 normal。
// 状态转换式告警：进区发 1 条 critical，出区发 1 条 info recovered；区内持续不重发。
// 2 tick 去抖：连续 2 tick 同区才转换（吸收 D-251 教训）；首 tick 静默（firstTick 惯例）。
package dashboard

import (
	"context"
	"fmt"
	"sync"
)

// cmcEquityZone D-252：净值告警三态。
type cmcEquityZone int

const (
	equityNormal   cmcEquityZone = iota // 正常
	equityWarning                       // equity ≤ 预警线
	equityCloseout                      // equity ≤ 强平线
)

// alertInserter D-252：告警插入接口（pgstore.Store 满足；测试用 fakeAlertInserter）。
type alertInserter interface {
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// cmcEquityAlertMachine D-252：净值告警状态机（内存态，重启重置，2 tick 去抖）。
type cmcEquityAlertMachine struct {
	store alertInserter

	mu           sync.Mutex
	confirmed    cmcEquityZone // 当前已告警的区
	pending      cmcEquityZone // 上次观察到的区（去抖候选）
	pendingCount int           // 连续观察到 pending 的次数
	firstTick    bool          // 首 tick 只记状态不告警
}

// newCMCEquityAlertMachine 创建状态机（首 tick 静默）。
func newCMCEquityAlertMachine(store alertInserter) *cmcEquityAlertMachine {
	return &cmcEquityAlertMachine{store: store, firstTick: true}
}

// classifyEquityZone D-252：equity → zone 分级。
// equity ≤ closeout → closeout 区；≤ warning → warning 区；否则 normal。
func classifyEquityZone(equity, warningLevel, closeoutLevel float64) cmcEquityZone {
	if equity <= closeoutLevel {
		return equityCloseout
	}
	if equity <= warningLevel {
		return equityWarning
	}
	return equityNormal
}

// check D-252：推进状态机一个 tick。线程安全（flushCMCWatch 从 timer goroutine 调用）。
func (m *cmcEquityAlertMachine) check(ctx context.Context, equity, warningLevel, closeoutLevel float64) {
	observed := classifyEquityZone(equity, warningLevel, closeoutLevel)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.firstTick {
		m.confirmed = observed
		m.pending = observed
		m.pendingCount = 0
		m.firstTick = false
		return
	}

	// 与 confirmed 相同：重置 pending，无转换
	if observed == m.confirmed {
		m.pending = observed
		m.pendingCount = 0
		return
	}

	// 与 pending 相同：增加计数，达 2 则转换
	if observed == m.pending {
		m.pendingCount++
		if m.pendingCount >= 2 {
			old := m.confirmed
			m.confirmed = observed
			m.pendingCount = 0
			m.emitTransition(ctx, old, observed)
		}
		return
	}

	// 与 pending 不同：重置 pending 为新观察值
	m.pending = observed
	m.pendingCount = 1
}

// emitTransition D-252：状态转换时插入告警（调用方已持锁，store 自身线程安全）。
func (m *cmcEquityAlertMachine) emitTransition(ctx context.Context, old, newZone cmcEquityZone) {
	if m.store == nil {
		return
	}
	switch newZone {
	case equityWarning:
		_ = m.store.InsertAlert(ctx, nil, "critical",
			"CMC equity ≤ 预警线 (equity ≤ warning_level_usd)")
	case equityCloseout:
		_ = m.store.InsertAlert(ctx, nil, "critical",
			"CMC equity ≤ 强平线 (equity ≤ closeout_level_usd)")
	case equityNormal:
		from := "预警线"
		if old == equityCloseout {
			from = "强平线"
		}
		_ = m.store.InsertAlert(ctx, nil, "info",
			fmt.Sprintf("CMC equity recovered (from %s)", from))
	}
}
