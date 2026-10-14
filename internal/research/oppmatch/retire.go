// retire.go —— 策略退役径（XASSET_CARRY 死角实证触发）：委任式自动下架闸。
// 并入 RunOnce 扫描拍零新定时器。判据（config 驱动不硬编码）：
//
//	app_config retire_delegate_enabled ∈ {0,false,off,disabled} → 一键全停（缺省启用）
//	app_config retire_idle_days    （缺省 14）— live 策略连续 Nd 零提案∧零动作∧零持仓∧零收益
//	app_config retire_pnl_floor_usd（缺省 -50）— 累计 total_pnl 越线（仍需零持仓才落）
//
// 退役走 SetStrategyState（decided_by='delegate-retire'）→ fin_strategy.retired +
// strategy_stats.disabled 同步（状态机唯一裁决，不再产提案不参评，台账保留可复活）。
// 每次退役 warn 告警推业主面；在飞提案/在持仓策略永不自动退（判据前置）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// delegateRetire live 策略退役扫描（runLifecycle 尾挂同拍）。
func delegateRetire(ctx context.Context, st *pgstore.Store) {
	if v, ok, err := st.AppConfigString(ctx, "retire_delegate_enabled"); err == nil && ok &&
		(v == "0" || v == "false" || v == "off" || v == "disabled") {
		return // 一键撤
	}
	idleDays := 14.0
	if v, ok, err := st.AppConfigFloat(ctx, "retire_idle_days"); err == nil && ok && v > 0 {
		idleDays = v
	}
	pnlFloor := -50.0
	if v, ok, err := st.AppConfigFloat(ctx, "retire_pnl_floor_usd"); err == nil && ok {
		pnlFloor = v
	}
	live, err := st.ListStrategiesByState(ctx, "live")
	if err != nil {
		log.Printf("[retire] list live: %v", err)
		return
	}
	for _, s := range live {
		hit, err := st.StrategyRetireVerdict(ctx, s, time.Duration(idleDays*24)*time.Hour, pnlFloor)
		if err != nil {
			log.Printf("[retire] verdict %s: %v", s, err)
			continue
		}
		if hit == "" {
			continue
		}
		if err := st.SetStrategyState(ctx, s, "retired", "delegate-retire",
			[]string{hit}); err != nil {
			log.Printf("[retire] set %s: %v", s, err)
			continue
		}
		if aerr := st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"策略 %s 委任退役（%s——台账保留可复活；retire_delegate_enabled 可一键关闸）",
			s, hit)); aerr != nil {
			log.Printf("[retire] alert %s: %v", s, aerr)
		}
		log.Printf("[retire] %s retired: %s", s, hit)
	}
}
