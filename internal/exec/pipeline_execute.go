// Package exec 的 pipeline_execute.go 实现 Execute 方法（从 pipeline.go 拆出，保 pipeline.go ≤300 行）。
package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"arb/internal/safego"
	"arb/internal/store/pgstore"
)

// Execute 执行机会（§13.1）。
// opportunityID + requestID 幂等：重复提交返回已有 position。
func (p *Pipeline) Execute(ctx context.Context, opportunityID, requestID, posType string, legs []LegSpec) (*ExecuteResult, error) {
	// 风控⓪：fail-fast 入口守卫（修复⑭ S4）。空 type 撞 positions_type_check 约束、nil legs 无
	// 下单意图——两者均不应落库，在触达任何 DB 写或风控查询前拒绝（生产数据完整性隐患）。
	if posType == "" {
		return nil, ErrEmptyPositionType
	}
	if len(legs) == 0 {
		return nil, ErrEmptyLegs
	}

	// D-195-B §4.1：request_id 幂等——重复提交返回已有 position，不新建。
	if existing, hit, err := p.checkRequestIdIdempotency(ctx, requestID); err != nil {
		return nil, err
	} else if hit {
		return existing, nil
	}

	// 风控①：持仓上限（D-058）。MaxOpenPositions>0 才启用；OPEN+OPENING+LEGGED 计数超限拒单。
	// check-then-act 无 DB 级原子性——当前单用户人工确认，接受窗口竞态（全自动才需约束）。
	if p.limits.MaxOpenPositions > 0 {
		n, err := p.store.CountOpenPositions(ctx)
		if err != nil {
			return nil, fmt.Errorf("count open positions: %w", err)
		}
		if n >= p.limits.MaxOpenPositions {
			return nil, ErrMaxOpenPositions
		}
	}

	// 风控②：幂等防重（D-058）。同机会已有未平仓持仓（OPEN/OPENING/LEGGED）拒绝重复执行，
	// 防双击/重试重复建仓；CLOSED/FAILED 可重入（新的执行意图）。requestID 级幂等需新列，暂以
	// opportunityID 级覆盖重复提交的主要危害，留待未来。
	dup, err := p.store.HasOpenPositionByOpportunity(ctx, opportunityID)
	if err != nil {
		return nil, fmt.Errorf("check existing position: %w", err)
	}
	if dup {
		return nil, ErrDuplicateExecution
	}

	// 风控③：下单前预检（D-121）：对每条腿调 executor.Precheck（如果实现了 Prechecker），
	// 任一腿预检失败 → 不建 position 不下单，直接返回错误（防 LEGGED）。
	// gate testnet 部分合约缺失（如 TRX_USDT）→ 不预检则一腿成交一腿失败→LEGGED。
	for _, leg := range legs {
		exe, ok := p.registry.Get(leg.VenueKey)
		if !ok {
			return nil, fmt.Errorf("precheck: no executor for venue %s", leg.VenueKey)
		}
		if pc, ok := exe.(Prechecker); ok {
			if err := pc.Precheck(ctx, Order{
				Symbol: leg.VenueSymbol,
				Side:   leg.Side,
				Qty:    leg.Qty,
				Venue:  leg.VenueKey,
			}); err != nil {
				return nil, fmt.Errorf("precheck leg %s: %w", leg.VenueKey, err)
			}
		}
	}

	// 风控④：D-159 W1a R1 S2.3 SWAPARB 精确组合门禁——真实名义额检查。
	// swapArbComboAllow 非 nil 且 posType=SWAPARB 时，建 position 前校验：
	//   1. (canonical, venueBuy, venueSell) 在 allowlist
	//   2. 双腿名义额 ≤ spec.MaxLongQty/MaxShortQty（>0 才校验）
	//   3. 双腿名义额合计 USD ≤ spec.MaxGrossExposureUSD（>0 才校验）
	// nil/空 = 未接线（gate 层 + dashboard 手动入口兜底）。
	if posType == "SWAPARB" && len(p.swapArbComboAllow) > 0 {
		if err := p.checkSwapArbComboAllow(ctx, opportunityID, legs); err != nil {
			return nil, err
		}
	}

	// 建 position(OPENING)——type 在创建时即写入（修复⑭ S4：原创建空 type 撞 positions_type_check，
	// 靠调用方事后 UpdatePositionMeta 补 type，空窗期约束已拒）。
	posID, err := p.store.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: opportunityID,
		Type:          posType,
		Status:        "OPENING",
		RequestID:     requestID,
		DedupKey:      opportunityID, // 当前 dedup_key 业务逻辑=opportunity_id（combo.Key 格式）
		Environment:   p.environment, // D-195-B：从 config 注入
	})
	if err != nil {
		return nil, fmt.Errorf("create position: %w", err)
	}

	// 双腿并发下单
	results := make([]LegResult, len(legs))
	var wg sync.WaitGroup
	for i, leg := range legs {
		wg.Add(1)
		idx, l := i, leg
		safego.Go("pipeline:leg-exec", func() {
			defer wg.Done()
			// D-195-B §4.3：client_order_id 确定性编码（attempt_no=1 首次）。
			coid := generateClientOrderID(requestID, posID, int16(idx), 1)
			results[idx] = p.executeLeg(ctx, l, coid)
		})
	}
	wg.Wait()

	// 判定最终状态
	filled := 0
	unknown := 0
	for _, r := range results {
		if r.Status == "FILLED" {
			filled++
		}
		if r.Status == "UNKNOWN" {
			unknown++
		}
	}

	var status string
	var isUnknown bool
	switch {
	case unknown > 0:
		// D-159 W1a R2：MT 超时→CLOSING+uncertainty_reason 过渡（SSOT I1，不新增 DB 枚举）。
		// 不平仓不假 FAILED——可能已成交，假 FAILED 会触发平仓=反向开新仓。
		status = "CLOSING"
		isUnknown = true
	case filled == len(legs):
		status = "OPEN"
	case filled == 0:
		status = "FAILED"
	default:
		status = "LEGGED"
	}

	// 回填 legs 到 DB（D-108 A1 fail-closed：腿写库失败 → error+critical 告警，孤儿不可留）
	// D-195-B §4.3：client_order_id + attempt 字段填充（提取到 persistOpenLegs 避免 pipeline.go 超限）。
	if err := p.persistOpenLegs(ctx, requestID, posID, legs, results); err != nil {
		return &ExecuteResult{PositionID: posID, Status: "FAILED", Legs: results}, err
	}

	// 更新 position 状态
	if err := p.store.UpdatePositionStatus(ctx, posID, status); err != nil {
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already terminal, skip status=%s (G4)", posID, status)
		} else {
			log.Printf("[exec] position %d status update: %v", posID, err)
		}
	}
	// D-159 W1a R2：UNKNOWN 过渡写 uncertainty_reason（status=CLOSING，供前端/对账识别）。
	// 识别 UNKNOWN position：status='CLOSING' AND uncertainty_reason IS NOT NULL
	// 前端/对账用此条件区分正常平仓中 vs UNKNOWN 待确认
	if isUnknown {
		var unknownLegs []string
		for i, r := range results {
			if r.Status == "UNKNOWN" {
				unknownLegs = append(unknownLegs, fmt.Sprintf("leg%d(%s)", i, legs[i].VenueKey))
			}
		}
		reason := fmt.Sprintf("MT OrderSend timeout: %s", strings.Join(unknownLegs, ", "))
		if err := p.store.SetPositionUncertainty(ctx, posID, reason); err != nil {
			log.Printf("[exec] position %d uncertainty write: %v", posID, err)
		}
		_ = p.store.InsertAlert(ctx, &posID, "critical",
			fmt.Sprintf("position %d UNKNOWN: %s (manual verify required)", posID, reason))
	}

	if status == "LEGGED" {
		// D-127 P3b：纯 CEX 组合 LEGGED 自动收平——对 FILLED 腿立即反向 MARKET 平仓，
		// 消除裸敞口（#380/#383 人工 35+ 分钟兜底）。含 MT 腿的组合不动（SWAPARB 有独立平仓链路）。
		if allCEXLegs(legs) {
			// D-159 W2a R3-L174：autoFlatten 走 close coordinator（不绕过 ClosePosition）。
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
					_ = p.store.InsertAlert(autoFlattenCtx, &posID, "critical",
						fmt.Sprintf("position %d LEGGED auto-flatten close coordinator failed: %v", posID, err))
				} else {
					_ = p.store.InsertAlert(autoFlattenCtx, &posID, "critical",
						fmt.Sprintf("position %d LEGGED auto-flattened (open legs closed, root cause: partial fill)", posID))
					status = "CLOSED"
				}
			} else {
				_ = p.store.InsertAlert(autoFlattenCtx, &posID, "critical",
					fmt.Sprintf("position %d LEGGED: %d/%d legs filled, auto-flatten incomplete", posID, filled, len(legs)))
			}
		} else {
			// critical 告警
			_ = p.store.InsertAlert(ctx, nil, "critical",
				fmt.Sprintf("position %d LEGGED: %d/%d legs filled", posID, filled, len(legs)))
		}
	}

	return &ExecuteResult{
		PositionID: posID,
		Status:     status,
		Legs:       results,
	}, nil
}
