// mt_health_attach.go —— D-085：MT venue attach 未就绪闸（boot 竞态假阳根治）。
// 病灶：broker_accounts.status='connected' 在 broker attach 完成前就落库（connect
// 早于 8514 符号 backfill，今晚 ICM 15032313 实拍 4min>bootGrace≈3min）→ venueSet
// 收录未 attach 的 venue → quote/swap/margin 必然无数据 → classifyAge 判 crit
// 「行情停更」+ attach 后「已恢复」双噪音（00:19/00:56 两次飞书实证）。
// 判据：venueSet 成员 ∈ connectedSet 且 ∉ AccountQueriers()（querier=attach 完成的
// 权威信号，broker_manager_orders.go attach→syncQueriers）。attaching 期间三指标
// healthy 零告警；超 mtHealthAttachTimeout 仍未 attach → quote 单条 warn 兜底
// （attach 卡死不静默，进程内常量非域值）。
package dashboard

import (
	"context"
	"log"
	"time"
)

// mtHealthAttachTimeout attach 兜底超时（进程内常量——符号 backfill 最坏量级 4min 实证，
// 取 15min 留足余量又不让卡死 attach 静默过夜）。
const mtHealthAttachTimeout = 15 * time.Minute

// attachAges 本轮 attaching venue → 首次观察为 attaching 以来的经过时长。
// 判据集：venueSet 成员 ∧ ∈connectedSet ∧ ∉attached（connectedSet 全 MT broker；
// qbus 残留路 non-broker 无 attach 概念不收）。attached 或离场 venue 清记录。
// 副作用：attachingSince 首见打点（持 m.mu，同 states 更新并发风格）。
func (m *MTHealthMonitor) attachAges(venueSet, connectedSet, attached map[string]bool, now time.Time) map[string]time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attachingSince == nil {
		m.attachingSince = make(map[string]time.Time)
	}
	out := make(map[string]time.Duration)
	for v := range venueSet {
		if !connectedSet[v] || attached[v] {
			continue
		}
		if _, ok := m.attachingSince[v]; !ok {
			m.attachingSince[v] = now
		}
		out[v] = now.Sub(m.attachingSince[v])
	}
	for v := range m.attachingSince {
		if attached[v] || !venueSet[v] {
			delete(m.attachingSince, v) // attach 完成或离场（stopped/断线）即清
		}
	}
	return out
}

// maybeAttachWarn attaching 超时的 warn 兜底告警——仅 healthy→warn 转换插一条
// （warn 持续不重发；attached 后由正常分类接管）。周末静默窗语义同 maybeAlert
// （转换被吞可接受：超时重见会随新一轮 attaching 评估再报）。
func (m *MTHealthMonitor) maybeAttachWarn(ctx context.Context, display string, prev, cur mtHealthMetric) {
	if cur != metricWarn || prev == cur || isMTWeekendQuietWindow(m.nowTime()) {
		return
	}
	msg := "MT5 数据监控：" + display + " attach 超时 " + mtHealthAttachTimeout.String() +
		" 仍未就绪（broker attach 卡死或 venue 侧阻塞）→ 检查网关/backfill"
	if err := m.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
		log.Printf("[mt-health] attach warn insert: %v", err)
	}
	log.Printf("[mt-health] warn: %s", msg)
}
