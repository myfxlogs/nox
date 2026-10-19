// rotation.go —— D-612/D-613 息差轮动执行径（earn_rotation 提案→所内赎转）。
// 闸序：rail → 持有窗（app_config）→ 活期在册实拉 → 产品面实拉（APR 复验——
// 补正① 放行判定禁凭表值）→ 净值差（目标−现仓−时滞分级机会成本−双程费，全
// app_config）→ 坑表 → 幂等键 → Redeem→Subscribe 两腿 + io 划转记账。
// 现仓/目标皆执行拍实拉；轮不动/不达标 → declined 如实落态不静默。
package dciexec

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/platform/errclass"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// 轮动判据键（app_config 全表驱动——判据不落码面）。
const (
	rotCfgMinEdgePP    = "rotation_min_edge_pp"    // 净值差阈（pp 点，缺省 0.5）
	rotCfgMinHoldHours = "rotation_min_hold_hours" // 最小持有窗（h，缺省 168=7d）
	rotCfgRoundtripBps = "rotation_roundtrip_bps"  // 申赎双程费（bps，缺省 0——活期申赎零费机理）
	rotCfgMinUSD       = "rotation_min_usd"        // 轮动尘额阈（缺省 100）
	rotCfgLagPrefix    = "rotation_lag_hours_"     // 时滞分级档（per venue；缺省 24h——
	// venue 产品面无机器可读赎回时滞字段，档值=实测台账占位可运维改，D-613②）
)

type rotationCfg struct {
	minEdgePP    float64
	minHold      time.Duration
	roundtripBps float64
	minUSD       float64
	lagHours     float64
}

// rotationCfg 读判据（行缺=缺省值；值坏=error 传 fail-closed 调用方）。
func (e *Executor) rotationCfgOf(ctx context.Context, venue string) (rotationCfg, error) {
	cfg := rotationCfg{minEdgePP: 0.5, minHold: 168 * time.Hour, minUSD: 100, lagHours: 24}
	if v, ok, err := e.Store.AppConfigFloat(ctx, rotCfgMinEdgePP); err != nil {
		return cfg, err
	} else if ok {
		cfg.minEdgePP = v
	}
	if v, ok, err := e.Store.AppConfigFloat(ctx, rotCfgMinHoldHours); err != nil {
		return cfg, err
	} else if ok {
		cfg.minHold = time.Duration(v * float64(time.Hour))
	}
	if v, ok, err := e.Store.AppConfigFloat(ctx, rotCfgRoundtripBps); err != nil {
		return cfg, err
	} else if ok {
		cfg.roundtripBps = v
	}
	if v, ok, err := e.Store.AppConfigFloat(ctx, rotCfgMinUSD); err != nil {
		return cfg, err
	} else if ok {
		cfg.minUSD = v
	}
	if v, ok, err := e.Store.AppConfigFloat(ctx, rotCfgLagPrefix+venue); err != nil {
		return cfg, err
	} else if ok {
		cfg.lagHours = v
	}
	return cfg, nil
}

// rotationNetEdge 净值差（APR 小数口径）：毛差 − 时滞机会成本 − 双程费。
// 时滞成本=赎回在途不计息窗 curAPR×lagH/holdH；双程费=bps→持有窗年化折算。
func rotationNetEdge(dstAPR, curAPR, lagHours, holdHours, roundtripBps float64) float64 {
	lagCost := curAPR * lagHours / holdHours
	feeCost := roundtripBps / 1e4 * 8760 / holdHours
	return dstAPR - curAPR - lagCost - feeCost
}

// goEarnRotation 所内轮动执行：活期仓实拉 → 同 venue 产品面实拉 → 净值差阈 →
// 赎旧申新。判断面全执行拍实拉（表值只做扫描层候选排序，D-613 补正①）。
func (e *Executor) goEarnRotation(ctx context.Context, p *pgstore.Proposal) (string, error) {
	leg := gate.DCILeg{Name: "EARN_ROT", Tmpl: p.TemplateID}
	ccy := "USDT"
	if v, ok := p.Params["ccy"].(string); ok && v != "" {
		ccy = v
	}
	rail, ok := e.earnRail(p.Venue)
	if !ok {
		if _, has := e.EarnCreds[p.Venue]; !has && e.EarnRails == nil {
			return "", e.gap(ctx, leg, "credential", p.Venue+":earn",
				"请配置 %s 凭据（earn 轮动写径）", p.Venue)
		}
		return "", fmt.Errorf("earn 轮动 rail 缺席 venue=%s（如实不执行）", p.Venue)
	}
	cfg, err := e.rotationCfgOf(ctx, p.Venue)
	if err != nil {
		return "", e.gap(ctx, leg, "runtime", "app_config",
			"系统侧病灶：轮动判据读败（fail-closed 不轮）：%v", err)
	}
	// 最小持有窗：最近 EARN 账本行距窗内 → declined（防费耗抖动，D-612③）。
	if ts, has, aerr := e.Store.LatestEarnActivityAt(ctx, p.Venue); aerr != nil {
		return "", e.gap(ctx, leg, "runtime", "earn_activity",
			"系统侧病灶：earn 活动史读败（fail-closed）：%v", aerr)
	} else if has && e.nowOr().Sub(ts) < cfg.minHold {
		e.Store.SetProposalStatus(ctx, p.ID, "declined")
		return fmt.Sprintf("%s 持有窗内不轮（最近 earn 活动 %s < %.0fh）",
			p.Venue, ts.UTC().Format("01-02 15:04Z"), cfg.minHold.Hours()), nil
	}
	// 活期在册实拉（locked/定期禁碰——rail Positions 已剔）。
	positions, err := rail.Positions(ctx, ccy)
	if err != nil {
		return "", e.gap(ctx, leg, "runtime", "earn_positions",
			"系统侧病灶：%s %s 活期持仓读败：%v", p.Venue, ccy, err)
	}
	if len(positions) == 0 {
		e.Store.SetProposalStatus(ctx, p.ID, "declined")
		return fmt.Sprintf("%s %s 无活期在册可轮", p.Venue, ccy), nil
	}
	// 产品面实拉（APR 复验——目标档判定禁凭 24h 表值）。
	prods, err := rail.Products(ctx, ccy)
	if err != nil {
		return "", e.gap(ctx, leg, "runtime", "earn_list",
			"系统侧病灶：%s 活期产品面读败：%v", p.Venue, err)
	}
	aprByID := map[string]float64{}
	var best *EarnProduct
	for i := range prods {
		pp := &prods[i]
		aprByID[pp.ProductID] = pp.APR
		if !pp.CanPurchase || pp.APR <= 0 {
			continue
		}
		if best == nil || pp.APR > best.APR {
			best = pp
		}
	}
	if best == nil {
		e.Store.SetProposalStatus(ctx, p.ID, "declined")
		return fmt.Sprintf("%s %s 无可购档（产品面零达标）", p.Venue, ccy), nil
	}
	// 坑表抑单（赎+申双 op；读败 fail-closed 同申购例）。
	for _, op := range []string{"earn_redeem", "earn_subscribe"} {
		pits, perr := e.Store.ListVenueRejects(ctx, p.Venue, op)
		if perr != nil {
			return "", e.gap(ctx, leg, "runtime", op,
				"系统侧病灶：轮动坑表读败（fail-closed）：%v", perr)
		}
		for i := range pits {
			if pp := &pits[i]; pp.Active(e.Now()) && pp.Action != "fix" {
				e.Store.SetProposalStatus(ctx, p.ID, "declined")
				return fmt.Sprintf("%s 轮动坑表抑单（%s:%s 窗内——%s）",
					p.Venue, op, pp.ErrorSignature, pitExpiry(pp)), nil
			}
		}
	}
	holdH := cfg.minHold.Hours()
	var rotated float64
	var notes []string
	for _, pos := range positions {
		curAPR, known := aprByID[pos.ProductID]
		if !known {
			notes = append(notes, fmt.Sprintf("跳过 %s（现仓产品 APR 不可判——fail-closed 不比）", pos.ProductID))
			continue
		}
		if pos.ProductID == best.ProductID {
			continue // 已在最优档
		}
		amt := pos.Amount
		if amt < cfg.minUSD || amt < best.MinPurchase {
			notes = append(notes, fmt.Sprintf("跳过 %s 额 %.4f<阈", pos.ProductID, amt))
			continue
		}
		net := rotationNetEdge(best.APR, curAPR, cfg.lagHours, holdH, cfg.roundtripBps)
		if net*100 < cfg.minEdgePP {
			notes = append(notes, fmt.Sprintf("%s 净值差 %.2fpp<阈 %.2fpp（%.2f%%→%.2f%% 扣时滞/费）",
				pos.ProductID, net*100, cfg.minEdgePP, curAPR*100, best.APR*100))
			continue
		}
		msg, rerr := e.rotateOne(ctx, p, leg, rail, ccy, pos, best, curAPR)
		if rerr != nil {
			return "", rerr // 败单留痕在 rotateOne 内（坑表+finish failed）
		}
		rotated += amt
		notes = append(notes, msg)
	}
	if rotated <= 0 {
		e.Store.SetProposalStatus(ctx, p.ID, "declined")
		return fmt.Sprintf("%s %s 无可轮仓：%v", p.Venue, ccy, notes), nil
	}
	if err := e.Store.SetProposalStatus(ctx, p.ID, "executed"); err != nil {
		return "", fmt.Errorf("提案态机推进: %w", err)
	}
	// 成交必达（warn 级同 earn 申购例——info 被 feishu 滤噪闸拦）。
	if err := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"EARN 轮动成交 %s：%.4f %s→%s（APR %.2f%%——rotation 归因）",
		p.Venue, rotated, ccy, best.ProductID, best.APR*100)); err != nil {
		log.Printf("[dciexec] 轮动成交 alert 落行败: %v", err)
	}
	return fmt.Sprintf("%s %s 轮动 %.4f→%s（APR %.2f%%）%v",
		p.Venue, ccy, rotated, best.ProductID, best.APR*100, notes), nil
}

// rotateOne 单仓赎转：幂等键 → 账本行 → Redeem → Subscribe → io 双腿。
func (e *Executor) rotateOne(ctx context.Context, p *pgstore.Proposal, leg gate.DCILeg,
	rail EarnRail, ccy string, pos EarnPosition, best *EarnProduct, curAPR float64) (string, error) {
	reqPrefix := fmt.Sprintf("earnrot|%s|%s|p%d", ccy, pos.ProductID, p.ID)
	if open, oerr := e.Store.DCIOrderOpenIntent(ctx, reqPrefix); oerr != nil {
		return "", oerr
	} else if open {
		return "", fmt.Errorf("提案#%d 轮动 %s 已受理（幂等防重）", p.ID, pos.ProductID)
	}
	attempt, err := e.Store.DCIOrderIntentCount(ctx, reqPrefix)
	if err != nil {
		return "", err
	}
	tenor := 0
	oid, created, err := e.Store.CreateDCIOrder(ctx, &pgstore.DCIOrder{
		RequestID: fmt.Sprintf("%s|%d", reqPrefix, attempt+1),
		Venue:     p.Venue, TemplateID: p.TemplateID, ProductRef: best.ProductID,
		Underlying: ccy, OptType: "EARN", InvestCcy: ccy,
		Qty: pos.Amount, TenorDays: &tenor, ProposalID: &p.ID,
	})
	if err != nil {
		return "", err
	}
	if !created {
		return "", fmt.Errorf("提案#%d 轮动 %s 已受理（request_id 幂等命中）", p.ID, pos.ProductID)
	}
	nonce := e.nowOr().UnixNano()
	redBody, err := rail.Redeem(ctx, pos.ProductID, pos.Amount)
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		if lerr := e.Store.LearnVenueReject(ctx, p.Venue, "earn_redeem",
			errclass.VenueRejectSignature(err.Error()), 24*time.Hour); lerr != nil {
			log.Printf("[dciexec] 轮动赎回坑落行败: %v", lerr)
		}
		return "", e.gap(ctx, leg, "runtime", "earn_redeem",
			"系统侧病灶：轮动赎回 %s venue 拒单：%v", pos.ProductID, err)
	}
	e.ioTransfer(ctx, p.Venue, ccy, pos.Amount, "earn", "spot",
		earnIOExtID(redBody, "redeemId", fmt.Sprintf("rot-red:%s:%d", pos.ProductID, nonce)), redBody)
	body, err := rail.Subscribe(ctx, best.ProductID, pos.Amount)
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		if lerr := e.Store.LearnVenueReject(ctx, p.Venue, "earn_subscribe",
			errclass.VenueRejectSignature(err.Error()), 24*time.Hour); lerr != nil {
			log.Printf("[dciexec] 轮动申购坑落行败: %v", lerr)
		}
		// 赎出未回申=资金中间态——warn 如实告（非静默；spot 面由 cash_mgmt 下拍承接）。
		if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"EARN 轮动半腿 %s：%.4f %s 已赎出 %s 申购 %s 败：%v",
			p.Venue, pos.Amount, ccy, pos.ProductID, best.ProductID, err)); aerr != nil {
			log.Printf("[dciexec] 轮动半腿 alert: %v", aerr)
		}
		return "", e.gap(ctx, leg, "runtime", "earn_subscribe",
			"系统侧病灶：轮动申购 %s venue 拒单：%v", best.ProductID, err)
	}
	e.ioTransfer(ctx, p.Venue, ccy, pos.Amount, "spot", "earn",
		fmt.Sprintf("rot-sub:%d:%s", p.ID, best.ProductID), body)
	e.finish(ctx, oid, "filled", body)
	return fmt.Sprintf("%s %.4f %s %s→%s（%.2f%%→%.2f%%）",
		p.Venue, pos.Amount, ccy, pos.ProductID, best.ProductID, curAPR*100, best.APR*100), nil
}
