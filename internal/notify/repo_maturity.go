// D-272 到期提醒 checkMaturity（从 repo_reminder.go 拆出，保 ≤300 行）。
// 与 D-270 周四提醒门控完全解耦——到期是客观事件。
package notify

import (
	"context"
	"fmt"
	"log"
	"time"
)

// checkMaturity D-272 到期提醒：查询到期未提醒的 open 记录，发 warn 级飞书告警。
// 幂等：每天最多提醒一次（last_maturity_reminded_at）。
// 与 D-270 门控完全解耦——到期是客观事件。
func (s *RepoReminderService) checkMaturity(ctx context.Context, now time.Time) error {
	trades, err := s.store.ListMaturedRepoTrades(ctx, now)
	if err != nil {
		return fmt.Errorf("list matured repo trades: %w", err)
	}
	for _, t := range trades {
		name := repoInstrumentName(t.InstrumentCode)
		msg := fmt.Sprintf("[逆回购到期] %s ¥%.0f 今日到期（资金已可用），请查看券商 App 实际到账金额并在前端确认结算",
			name, t.PrincipalCny)
		if s.alertFn != nil {
			s.alertFn("warn", msg)
		}
		if err := s.store.UpdateMaturityReminded(ctx, t.ID); err != nil {
			log.Printf("[repo_reminder] update maturity reminded id=%d: %v", t.ID, err)
		}
	}
	return nil
}

// repoInstrumentName code→name 映射（与 dashboard.instrumentNames 一致，notify 包本地副本避免依赖）。
func repoInstrumentName(code string) string {
	switch code {
	case "204001":
		return "GC001"
	case "204007":
		return "GC007"
	case "204014":
		return "GC014"
	case "204028":
		return "GC028"
	default:
		return code
	}
}
