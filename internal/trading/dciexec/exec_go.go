// exec_go.go —— 提案执行主径（exec.go 棘轮拆件，行数门禁）：
// kbSuffix 拒单消费闸 + Go/GoProposal/DeclineProposal 口令执行面（纯移动零语义改）。
package dciexec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"nox/internal/kbsearch"
	"nox/internal/risk/gate"
)

// kbSuffix 拒单消费闸：败因签名对拍经验库条目 → ` kb:<id> <summary>` 附行；
// 零命中/读败 → ""（fail-open，绝不阻塞告警主径；热径纯关键词对拍零 LLM）。
func (e *Executor) kbSuffix(sig string) string {
	if e.KBDir == "" || sig == "" {
		return ""
	}
	return kbsearch.Suffix(e.KBDir, sig)
}

// Go 裸口令主径：最新 pending 可执行提案→GoProposal 绑 id 执行（D-189③）。
func (e *Executor) Go(ctx context.Context) (string, error) {
	var tmpls []string
	for t := range gate.ExecTemplates {
		tmpls = append(tmpls, t)
	}
	for t := range gate.EarnTemplates {
		tmpls = append(tmpls, t)
	}
	p, err := e.Store.LatestPendingProposal(ctx, tmpls)
	if err != nil {
		return "", err
	}
	// 裸 go 跳休眠提案取次席。
	if p != nil {
		if p, err = e.skipDormant(ctx, p, tmpls); err != nil {
			return "", err
		}
	}
	if p == nil {
		return "", fmt.Errorf("无可执行 pending 提案（fin_proposal 空——待 oppmatch 扫描产出）")
	}
	msg, err := e.GoProposal(ctx, p.ID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("提案#%d %s：%s", p.ID, p.TemplateID, msg), nil
}

// GoProposal —— [exec-entry:named-gate:CheckDCI] D-262 M4.3②：DCI 提案执行
// （「go N」口令/RPC 同径）：态机闸（proposed 才执行，
// expired/declined 拒单带因）+模板→腿子集+订单回链 proposal_id；
// strike/qty 实时闸复验保留——提案 params 快照是审计锚非钉价。
func (e *Executor) GoProposal(ctx context.Context, id int64) (string, error) {
	// D-620⑤ 真金双径归一：提案执行（DCI/earn 全真 venue）首步过控制面——
	// nil=未接线拒、!allowed=拒带因、mode≠LIVE=拒（kill 覆盖 DCI/earn 写面）。
	if e.ModeGate == nil {
		return "", fmt.Errorf("提案#%d 控制面未接线", id)
	}
	mode, allowed, gateReason, gerr := e.ModeGate.CheckGate(ctx)
	if gerr != nil || !allowed {
		return "", fmt.Errorf("提案#%d 控制面拒执行（mode=%s reason=%s err=%v）", id, mode, gateReason, gerr)
	}
	if mode != gate.ModeLive {
		return "", fmt.Errorf("提案#%d 模式=%s 非 LIVE——真金径拒执行", id, mode)
	}
	p, err := e.Store.GetProposal(ctx, id)
	if err != nil {
		return "", fmt.Errorf("提案#%d 查询败: %w", id, err)
	}
	// D-593 纸面先行闸（补洞②——提案→executed 唯一收口前置；earn/resume
	// 回环同径自盖）。证据域=paper_structures ∩ paper_spot_carry.structure。
	ok, why, gerr := e.Store.PaperFirstGate(ctx, p.TemplateID, func() (bool, error) {
		return e.Store.HasPaperEvidence(ctx, p.TemplateID)
	})
	if gerr != nil {
		return "", fmt.Errorf("提案#%d 纸面闸读败: %w", id, gerr)
	}
	if !ok {
		return "", fmt.Errorf("提案#%d 纸面先行闸拒: %s", id, why)
	}
	// D-262 M4.3②：检面归 risk/gate 单包（命名闸 CheckDCI；读库 fail-closed 留本层）。
	card, cerr := e.Store.GetOppEval(ctx, id)
	if cerr != nil {
		return "", fmt.Errorf("提案#%d 评估卡读败（拒单）: %w", id, cerr)
	}
	// 休眠轨读败放行（不扩闸）——约束读败放行同例。
	dormantHit := false
	if dormant, derr := e.dormantPairs(ctx); derr == nil {
		dormantHit = dormant[p.TemplateID+"@"+p.Venue]
	}
	var verdict string
	if card != nil {
		verdict = card.Verdict
	}
	if reason := gate.CheckDCI(gate.DCIProposalView{
		ID: id, TemplateID: p.TemplateID, Venue: p.Venue, Status: p.Status,
		HasCard: card != nil, Verdict: verdict, Dormant: dormantHit,
	}); reason != "" {
		return "", errors.New(reason)
	}
	// R11② 执行租约：同 proposal_id 120s 内有单即拒（双击/重发防双成交）。
	if oid, err := e.Store.RecentDCIOrderByProposal(ctx, id, 120*time.Second); err != nil {
		return "", err
	} else if oid > 0 {
		return "", fmt.Errorf("提案#%d 刚执行过（订单#%d）拒单", id, oid)
	}
	// D-286/D-612：earn 族提案执行径分派（闸序已过 CheckDCI+租约）——
	// earn_rotation=所内赎转径；其余 earn 族=申购径（venue 产品非腿集）。
	if gate.EarnTemplates[p.TemplateID] {
		if p.TemplateID == "earn_rotation" {
			return e.goEarnRotation(ctx, p)
		}
		return e.goEarn(ctx, p)
	}
	legs := gate.ExecTemplates[p.TemplateID]
	// 腿执行面按 venue 分派（okx DCI：quote→trade 两段式+funding 扣款户）；
	// binance 径凭据闸照旧，okx 凭据闸在 tryLegOKX 内（credential 缺口同形）。
	try := e.tryLeg
	if p.Venue == "okx" {
		try = e.tryLegOKX
	} else if e.BnKey == "" {
		return "", e.gap(ctx, gate.CallLeg, "credential", "binance:spot",
			"请配置 binance 凭据（DCI 申购写径）")
	}
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return "", err
	}
	_, tenorCap := gate.TenorCapOf(cons)
	// D-644：share=venue 粒度读面（custody_share@<venue> 优先，无键回落全局——
	// okx 池=纯托管钱 ×1.0，binance 共享池 ×0.34）。
	share := gate.CustodyShareOf(cons, p.Venue)
	if share == nil {
		return "", e.gap(ctx, gate.CallLeg, "constraint", "custody_share",
			"请喂约束 custody_share 或 custody_share@%s（托管可动份额上限）", p.Venue)
	}
	spot, err := e.spotPriceVenue(ctx, p.Venue, "BTCUSDT")
	if err != nil {
		if errors.Is(err, errSpotNoBase) {
			return "", e.gap(ctx, gate.CallLeg, "credential", "dual_quote",
				"%s BTC 现货价读径 base/凭据行缺——请配置 %s API base（%v）", p.Venue, p.Venue, err)
		}
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, gate.CallLeg, "permission", "dual_quote",
				"%s BTC 现货价读取失败：%v", p.Venue, err)
		}
		return "", e.gap(ctx, gate.CallLeg, "runtime", "dual_quote",
			"系统侧病灶：%s BTC 现货价读取失败：%v", p.Venue, err)
	}
	var msgs []string
	var succeeded bool
	sigma, serr := e.sigmaDaily(ctx, p.Venue) // D-526+D-651：提案 venue σ 实算（禁 binance 兜）
	for _, l := range legs {
		k, tmax, ok := gate.SigmaTailParams(cons, l.OptType)
		if !ok || serr != nil { // 界参/σ 缺=constraint 缺口拒单（同 custody_share 例）
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.Name,
				e.gap(ctx, l, "constraint", l.BoundSubject(),
					"请喂约束 dci_*_otm_k/dci_tenor_max_days 或 σ 源可用（%v）——D-526 尾险闸 fail-closed", serr)))
			continue
		}
		// 资产域 cap 第二闸（custody_share:<ccy>——业主令「BTC 只能用 1/3」
		// constraint#41 消费面）：venue 池份额×资产份额双闸取小；无键行=不设限。
		eff := *share
		if as := gate.CustodyAssetShareOf(cons, l.QtyCcy); as != nil && *as < eff {
			eff = *as
		}
		msg, err := try(ctx, l, eff, tenorCap,
			spot*gate.SigmaBoundRatio(l.OptType, sigma, k, tmax), spot, sigma, &p.ID)
		if err != nil {
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.Name, err))
			continue
		}
		succeeded = true
		msgs = append(msgs, msg)
	}
	if !succeeded {
		// R10④：失败因内嵌 err——venue 原文拒绝体随回执直发不藏。
		return strings.Join(msgs, "；"),
			fmt.Errorf("提案#%d 无腿执行成功：%s", id, strings.Join(msgs, "；"))
	}
	if err := e.Store.SetProposalStatus(ctx, id, "executed"); err != nil {
		return "", fmt.Errorf("提案态机推进: %w", err)
	}
	return strings.Join(msgs, "；"), nil
}

// DeclineProposal 「不go N」→declined（业主拒批态机推进）。
func (e *Executor) DeclineProposal(ctx context.Context, id int64) (string, error) {
	p, err := e.Store.GetProposal(ctx, id)
	if err != nil {
		return "", fmt.Errorf("提案#%d 查询败: %w", id, err)
	}
	if p.Status != "proposed" {
		return fmt.Sprintf("提案#%d 已 %s", id, p.Status), nil
	}
	if err := e.Store.SetProposalStatus(ctx, id, "declined"); err != nil {
		return "", err
	}
	return fmt.Sprintf("提案#%d 已 decline", id), nil
}
