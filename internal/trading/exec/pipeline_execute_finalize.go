// pipeline_execute_finalize.go —— Execute 执行后处理段（UNKNOWN 过渡/腿量不匹配告警/
// LEGGED 自动收平），自 pipeline_execute.go 纯搬移零逻辑变更（D-026 R1 ≤300 契约）。
package exec

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// finalizeOpenPostLegs 在状态判定后执行：
//   - UNKNOWN（MT/CEX 下单超时）→ CLOSING + uncertainty_reason + critical（禁假 FAILED）；
//   - qtyMismatch（D-026 ②）→ critical「腿量不匹配」（不匹配的 OPEN 比 LEGGED 更坏）；
//   - LEGGED → 纯 CEX 组合 autoFlatten 自动收平（CLOSED），混合腿 critical 告警人工兜底。
//
// 返回 (最终 status, err)：flatten 成功 → CLOSED；err 非空 → 调用方保留判定 status（不虚报）。
func (p *Pipeline) finalizeOpenPostLegs(ctx context.Context, posID int64, legs []LegSpec, results []LegResult, status string, filled int, isUnknown, qtyMismatch bool) (string, error) {
	if isUnknown {
		var unknownLegs []string
		for i, r := range results {
			if r.Status == "UNKNOWN" {
				unknownLegs = append(unknownLegs, fmt.Sprintf("腿%d（%s）", i, legs[i].VenueKey))
			}
		}
		reason := fmt.Sprintf("MT 下单超时：%s", strings.Join(unknownLegs, ", "))
		if err := p.store.SetPositionUncertainty(ctx, posID, reason); err != nil {
			log.Printf("[exec] position %d uncertainty write: %v", posID, err)
		}
		if err := p.store.InsertPositionAlert(ctx, posID, "critical",
			fmt.Sprintf("持仓 #%d 平仓结果未知：%s——需人工到交易所核对", posID, reason)); err != nil {
			log.Printf("[exec] pos %d alert write: %v", posID, err)
		}
	}

	if qtyMismatch {
		if err := p.store.InsertPositionAlert(ctx, posID, "critical",
			fmt.Sprintf("持仓 #%d 腿量不匹配：FILLED 腿有效量偏差超容差（%d/%d 腿 FILLED）——不判 OPEN，落 unwind", posID, filled, len(legs))); err != nil {
			log.Printf("[exec] pos %d alert write: %v", posID, err)
		}
	}
	if status == "LEGGED" {
		// 纯 CEX 组合 LEGGED 自动收平——对 FILLED 腿立即反向 MARKET 平仓，
		// 消除裸敞口（#380/#383 人工 35+ 分钟兜底）。含 MT 腿的组合不动（SWAPARB 有独立平仓链路）。
		if allCEXLegs(legs) {
			// autoFlatten 走 close coordinator（不绕过 ClosePosition）。
			// autoFlattenLEGGED 只负责反向平仓 I/O，CLOSED 由 ClosePosition 统一处理。
			autoFlattenCtx := context.WithoutCancel(ctx) // 收平不随 Execute 超时中断
			flattened, flattenErr := p.autoFlattenLEGGED(autoFlattenCtx, posID, legs, results)
			if flattenErr != nil {
				log.Printf("[exec] position %d LEGGED auto-flatten failed: %v (manual fallback)", posID, flattenErr)
			}
			if flattened {
				// 走 close coordinator：ClaimClose + 写 CLOSE leg + 残量校验 + CLOSED
				if err := p.ClosePosition(autoFlattenCtx, posID); err != nil {
					log.Printf("[exec] position %d auto-flatten ClosePosition: %v", posID, err)
					if err := p.store.InsertPositionAlert(autoFlattenCtx, posID, "critical",
						fmt.Sprintf("持仓 #%d 单腿成交裸露风险：自动平仓处置失败（%v）——需人工处理", posID, err)); err != nil {
						log.Printf("[exec] pos %d alert write: %v", posID, err)
					}
				} else {
					if err := p.store.InsertPositionAlert(autoFlattenCtx, posID, "critical",
						fmt.Sprintf("持仓 #%d 单腿成交裸露：已自动平掉其余腿（根因=部分成交）", posID)); err != nil {
						log.Printf("[exec] pos %d alert write: %v", posID, err)
					}
					status = "CLOSED"
				}
			} else {
				if err := p.store.InsertPositionAlert(autoFlattenCtx, posID, "critical",
					fmt.Sprintf("持仓 #%d 单腿成交裸露：%d/%d 腿已成交，自动平仓不完整——需人工处理", posID, filled, len(legs))); err != nil {
					log.Printf("[exec] pos %d alert write: %v", posID, err)
				}
			}
		} else {
			// 含 MT 腿混合组合走独立平仓链（实测秒级自愈）——不即时 critical，
			// 登记延迟判定由 reconcile.Run 惰性复查：CLOSED→info、逾期仍裸→critical。
			if err := p.store.DeferLeggedAlert(ctx, posID, filled, len(legs)); err != nil {
				log.Printf("[exec] pos %d legged defer write: %v", posID, err)
			}
		}
	}
	return status, nil
}
