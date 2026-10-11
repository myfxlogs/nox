// exec.go —— D-191 R3：轮式 DCI 执行轨（binance SAPI 写径，covered-call+cash-put 双向）。
// 触发=业主「go」口令；strike 规则化 %OTM 闸（CALL≥spot×1.10/PUT≤spot×0.93，
// spot=执行时实时读）；期限上限=custody_redeemable.value_num；qty=币安实时余额
// ×custody_share（禁读 personal_positions 静态行）；无达标档→gap 如实报不降级凑单；
// 幂等=fin_dci_order.request_id UNIQUE 单次尝试不重试；失败→fin_condition_gap 请求。
package dciexec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// sign HMAC-SHA256 hex（binance sapi 同法，包内独立实现 P3 不适用）。
func sign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// Store 执行面依赖（*pgstore.Store 实现；测试走 fake）。
type Store interface {
	ListCapabilityConstraints(ctx context.Context) ([]pgstore.CapabilityConstraint, error)
	ListDCIQuotes(ctx context.Context, venue, underlying, optType, investCcy string, maxTenorDays int, strikeBound float64) ([]pgstore.CexDCIQuote, error)
	CreateDCIOrder(ctx context.Context, o *pgstore.DCIOrder) (int64, bool, error)
	RecentDCIOrderByProposal(ctx context.Context, proposalID int64, window time.Duration) (int64, error)
	ListFailedProposalOrders(ctx context.Context) ([]pgstore.FailedProposalOrder, error)
	OutstandingDCIProduct(ctx context.Context, productRef, optType string) (bool, error)
	DCIOrderLegCount(ctx context.Context, proposalID int64, optType, investCcy string) (int, error)
	FinishDCIOrder(ctx context.Context, id int64, status, response string) error
	InsertPersonalPosition(ctx context.Context, asset, kind, priceSymbol, note string, qty *float64, openedAt *time.Time) (int64, error)
	OutstandingDCIQty(ctx context.Context, venue, investCcy string) (float64, error)
	ListOpenPersonalPositions(ctx context.Context) ([]pgstore.PersonalPosition, error)
	UpdatePersonalPositionQtyNote(ctx context.Context, id int64, qty float64, note string) error
	OpenConditionGap(ctx context.Context, g *pgstore.ConditionGap) (int64, error)
	RequestConditionGap(ctx context.Context, id int64) error
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	// D-189：提案绑定面。
	LatestPendingProposal(ctx context.Context, templates []string) (*pgstore.Proposal, error)
	GetProposal(ctx context.Context, id int64) (*pgstore.Proposal, error)
	SetProposalStatus(ctx context.Context, id int64, status string) error
}

// Executor binance DCI 申购执行器（凭据由装配侧从 exchange_configs 注入）。
type Executor struct {
	Store   Store
	BnKey   string
	BnSec   string
	Sapi    string // sapi base（申购写径）
	APIBase string // api base（余额/现货读径）
	Client  *http.Client
	Now     func() time.Time // 测试可覆
	// 测试注入面（nil=真实 HTTP 实现）：
	PriceFn   func(ctx context.Context, symbol string) (float64, error)
	BalanceFn func(ctx context.Context, asset string) (float64, error) // 现货 free+locked
	EarnFn    func(ctx context.Context, asset string) ([]EarnPosition, error)
	RedeemFn  func(ctx context.Context, productID string, amount float64) error
	StreamFn  func() bool                                                         // 流存活闸（nil=dephealth 实读）
	HoldingFn func(ctx context.Context, productRef, optType string) (bool, error) // venue 持仓读（D-198 返修①；nil=实 GET）
}

// execTemplates 可执行提案模板→腿映射（D-189：提案模板=腿子集；
// 其余模板提案可批不执行「模板未接执行轨」）。
var execTemplates = map[string][]leg{
	"dci_sell_high_covered_call": {callLeg},
	"dci_buy_low":                {putLeg},
}

// ExecTemplateVenues 可执行提案模板→执行 venue（D-193① venue 悬空自检数据源；
// 本 executor=binance 单所写径——venue 集从腿面衍生非手抄）。
func ExecTemplateVenues() map[string]string {
	out := make(map[string]string, len(execTemplates))
	for t := range execTemplates {
		out[t] = "binance"
	}
	return out
}

// Go 裸口令主径：最新 pending 可执行提案→GoProposal 绑 id 执行（D-189③）。
func (e *Executor) Go(ctx context.Context) (string, error) {
	var tmpls []string
	for t := range execTemplates {
		tmpls = append(tmpls, t)
	}
	p, err := e.Store.LatestPendingProposal(ctx, tmpls)
	if err != nil {
		return "", err
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

// GoProposal 指定提案执行（「go N」口令/RPC 同径）：态机闸（proposed 才执行，
// expired/declined 拒单带因）+模板→腿子集+订单回链 proposal_id；
// strike/qty 实时闸复验保留——提案 params 快照是审计锚非钉价。
func (e *Executor) GoProposal(ctx context.Context, id int64) (string, error) {
	p, err := e.Store.GetProposal(ctx, id)
	if err != nil {
		return "", fmt.Errorf("提案#%d 查询败: %w", id, err)
	}
	if p.Status != "proposed" {
		return "", fmt.Errorf("提案#%d 已 %s（拒单）", id, p.Status)
	}
	legs, ok := execTemplates[p.TemplateID]
	if !ok {
		return "", fmt.Errorf("提案#%d 模板 %s 未接执行轨（提案可批执行面未接线）", id, p.TemplateID)
	}
	// R11② 执行租约：同 proposal_id 120s 内有单即拒（双击/重发防双成交）。
	if oid, err := e.Store.RecentDCIOrderByProposal(ctx, id, 120*time.Second); err != nil {
		return "", err
	} else if oid > 0 {
		return "", fmt.Errorf("提案#%d 刚执行过（订单#%d）拒单", id, oid)
	}
	if e.BnKey == "" {
		return "", e.gap(ctx, callLeg, "credential", "binance:spot",
			"请配置 binance 凭据（DCI 申购写径）")
	}
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return "", err
	}
	share, tenorCap := tenorCapOf(cons)
	if share == nil {
		return "", e.gap(ctx, callLeg, "constraint", "custody_share",
			"请喂约束 custody_share（托管可动份额上限）")
	}
	spot, err := e.spotPrice(ctx, "BTCUSDT")
	if err != nil {
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, callLeg, "permission", "dual_quote",
				"binance BTC 现货价读取失败：%v", err)
		}
		return "", e.gap(ctx, callLeg, "runtime", "dual_quote",
			"系统侧病灶：BTC 现货价读取失败：%v", err)
	}
	var msgs []string
	var succeeded bool
	cb, pb := OTMBoundRatios(cons)
	for _, l := range legs {
		ratio := l.ratioOf(cb, pb)
		if ratio == nil { // R9：界值行缺=constraint 缺口拒单（同 custody_share 例）
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.name,
				e.gap(ctx, l, "constraint", l.boundSubject(),
					"请喂约束 %s（%s腿 OTM 闸界比率）", l.boundSubject(), l.name)))
			continue
		}
		msg, err := e.tryLeg(ctx, l, *share, tenorCap, spot*(*ratio), spot, &p.ID)
		if err != nil {
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.name, err))
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

// GoWheel 轮式双腿（旧 Go 全腿径保留为内部径——D-191 R3 wheel 语义；
// 提案绑定径=GoProposal，无提案场景此径不再口令可达）。
func (e *Executor) GoWheel(ctx context.Context) (string, error) {
	if e.BnKey == "" {
		return "", e.gap(ctx, callLeg, "credential", "binance:spot",
			"请配置 binance 凭据（DCI 申购写径）")
	}
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return "", err
	}
	share, tenorCap := tenorCapOf(cons)
	if share == nil {
		return "", e.gap(ctx, callLeg, "constraint", "custody_share",
			"请喂约束 custody_share（托管可动份额上限）")
	}
	// 现货执行时实时读（strike %OTM 闸源）。
	spot, err := e.spotPrice(ctx, "BTCUSDT")
	if err != nil {
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, callLeg, "permission", "dual_quote",
				"binance BTC 现货价读取失败：%v", err)
		}
		return "", e.gap(ctx, callLeg, "runtime", "dual_quote",
			"系统侧病灶：BTC 现货价读取失败：%v", err)
	}
	var msgs []string
	cb, pb := OTMBoundRatios(cons)
	for _, l := range []leg{callLeg, putLeg} {
		ratio := l.ratioOf(cb, pb)
		if ratio == nil {
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.name,
				e.gap(ctx, l, "constraint", l.boundSubject(),
					"请喂约束 %s（%s腿 OTM 闸界比率）", l.boundSubject(), l.name)))
			continue
		}
		msg, err := e.tryLeg(ctx, l, *share, tenorCap, spot*(*ratio), spot, nil)
		if err != nil {
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.name, err))
			continue
		}
		msgs = append(msgs, msg)
	}
	return strings.Join(msgs, "；"), nil
}

// poolBalance 托管池余额=现货(free+locked)+赚币子账持仓（R6 池口径；
// outstanding DCI 锁仓在对账侧回填，下单 qty 不含——在外单不再复投）。
func (e *Executor) poolBalance(ctx context.Context, asset string) (float64, error) {
	bal, err := e.freeBalance(ctx, asset)
	if err != nil {
		return 0, err
	}
	eps, err := e.earnPositionsOf(ctx, asset)
	if err != nil {
		return 0, err
	}
	for _, p := range eps {
		bal += p.Amount
	}
	return bal, nil
}

func (e *Executor) earnPositionsOf(ctx context.Context, asset string) ([]EarnPosition, error) {
	if e.EarnFn != nil {
		return e.EarnFn(ctx, asset)
	}
	return e.earnPositions(ctx, asset)
}

func (e *Executor) redeem(ctx context.Context, productID string, amount float64) error {
	if e.RedeemFn != nil {
		return e.RedeemFn(ctx, productID, amount)
	}
	return e.redeemFlexible(ctx, productID, amount)
}

// redeemShortfall 活期赎回缺口：逐 flexible 持仓赎至凑齐 need（locked 定期不赎——
// 强制赎回罚息面禁动）；返回实赎量。
func (e *Executor) redeemShortfall(ctx context.Context, asset string, need float64) (float64, error) {
	eps, err := e.earnPositionsOf(ctx, asset)
	if err != nil {
		return 0, err
	}
	var done float64
	for _, p := range eps {
		if p.Locked || need-done <= 0 {
			continue
		}
		amt := p.Amount
		if amt > need-done {
			amt = need - done
		}
		if err := e.redeem(ctx, p.ProductID, amt); err != nil {
			return done, err
		}
		done += amt
	}
	return done, nil
}

// tryLeg 单腿：池余额×share→qty、spot 不足→活期赎回缺口→重读、候选档 strike 闸
// →最优、幂等→签名 POST→回执（R6 前置 redeem 步）。
func (e *Executor) tryLeg(ctx context.Context, l leg, share float64, tenorCap int, bound, spot float64, propID *int64) (string, error) {
	bal, err := e.poolBalance(ctx, l.qtyCcy)
	if err != nil {
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, l, "permission", "earn_position",
				"请开通 binance 赚币子账查询权限（%s 池余额读败：%v）", l.qtyCcy, err)
		}
		return "", e.gap(ctx, l, "runtime", "earn_position",
			"系统侧病灶：%s 池余额读败：%v", l.qtyCcy, err)
	}
	qty := bal * share
	if qty <= 0 {
		return fmt.Sprintf("%s腿跳过（%s 可动=0）", l.name, l.qtyCcy), nil
	}
	// 前置赎回步：现货可动不足→赚币活期赎回缺口→重读现货（单次步零重试）。
	spotFree, err := e.freeBalance(ctx, l.qtyCcy)
	if err != nil {
		return "", fmt.Errorf("%s 现货余额读败：%w", l.qtyCcy, err)
	}
	if spotFree < qty {
		redeemed, rerr := e.redeemShortfall(ctx, l.qtyCcy, qty-spotFree)
		if rerr != nil {
			if venueErrKind(rerr) == "permission" {
				return "", e.gap(ctx, l, "permission", "earn_redeem",
					"请开通 binance 赚币活期赎回权限（%s 赎回败：%v）", l.qtyCcy, rerr)
			}
			return "", e.gap(ctx, l, "runtime", "earn_redeem",
				"系统侧病灶：%s 活期赎回败：%v", l.qtyCcy, rerr)
		}
		if redeemed > 0 {
			if spotFree, err = e.freeBalance(ctx, l.qtyCcy); err != nil {
				return "", fmt.Errorf("%s 赎回后现货重读败：%w", l.qtyCcy, err)
			}
		}
		if spotFree < qty {
			return "", e.gap(ctx, l, "holding", l.qtyCcy,
				"%s 现货可动不足（池算 qty %.6f、赎回后现货 %.6f）", l.qtyCcy, qty, spotFree)
		}
	}
	qs, err := e.Store.ListDCIQuotes(ctx, "binance", "BTC", l.optType, l.investCcy, tenorCap, bound)
	if err != nil {
		return "", err
	}
	var q *pgstore.CexDCIQuote
	var sawStrikeOK bool
	for i := range qs {
		if !l.strikePass(&qs[i], bound) {
			continue
		}
		sawStrikeOK = true
		if qs[i].OrderRef == "" {
			continue // R10③：order_ref 缺行跳次档（申购 orderId 参源）
		}
		if qs[i].MinAmount == nil {
			continue // R12：min_amount 缺行跳次档（floor 位真源缺=不可发参）
		}
		q = &qs[i]
		break
	}
	if q == nil {
		if sawStrikeOK {
			return "", e.gap(ctx, l, "runtime", "dual_quote",
				"系统侧病灶：%s腿无可申购参数档（达标档缺 order_ref/min_amount——采集面未落）", l.name)
		}
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"%s腿无达标档在售（spot %.0f，strike 界 %s）", l.name, spot, l.boundText(bound))
	}
	// R11/R12：depositAmount floor 至 min_amount 小数位；界外 fail-closed
	// 如实败（不补不截——补到 min=多交、截到 max=少投，皆改提案语义）。
	deposit := floorTo(qty, decOf(*q.MinAmount))
	if deposit < *q.MinAmount || (q.MaxAmount != nil && deposit > *q.MaxAmount) {
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"系统侧病灶：%s腿 depositAmount %v 界外（qty %.8f floor 至 dec%d；档 min %v max %v——不补不截如实败）",
			l.name, deposit, qty, decOf(*q.MinAmount), amtTxt(q.MinAmount), amtTxt(q.MaxAmount))
	}
	now := e.Now()
	// D-198 返修②：reqID 尾加尝试序号 n（同 proposal_id 该腿订单行数+1）——
	// 败单行不再 ON CONFLICT 吞续执、二次 go 同市况不再假「已受理」。
	attempt := 1
	if propID != nil {
		if c, cerr := e.Store.DCIOrderLegCount(ctx, *propID, l.optType, l.investCcy); cerr == nil {
			attempt = c + 1
		}
	}
	reqID := fmt.Sprintf("dci|%s|%s|%s|%.8f|%d", l.name, q.ProductRef, delivDay(q), deposit, attempt)
	oid, created, err := e.Store.CreateDCIOrder(ctx, &pgstore.DCIOrder{
		RequestID: reqID, Venue: "binance", TemplateID: l.tmpl,
		ProductRef: q.ProductRef, Underlying: "BTC", OptType: l.optType, Strike: q.Strike,
		InvestCcy: q.InvestCcy, Qty: deposit, TenorDays: q.TenorDays, ProposalID: propID})
	if err != nil {
		return "", err
	}
	if !created {
		return fmt.Sprintf("%s腿已受理（request_id 防重）", l.name), nil
	}
	// R14：申购前实时重取 orderId+canPurchase（DB order_ref 降格候选证据）。
	orderRef, rerr := e.refreshOrderRef(ctx, l, q.ProductRef)
	if rerr != nil {
		e.finish(ctx, oid, "failed", rerr.Error())
		return "", e.gap(ctx, l, "runtime", "dual_quote",
			"系统侧病灶：%s腿产品已下架（%s 重取：%v）", l.name, q.ProductRef, rerr)
	}
	body, err := e.post(ctx, q.ProductRef, orderRef, deposit, now)
	if isRefreshErr(err) { // 「refresh」类拒因→一次再刷新重提（第二次如实 gap）。
		if orderRef, rerr = e.refreshOrderRef(ctx, l, q.ProductRef); rerr == nil {
			body, err = e.post(ctx, q.ProductRef, orderRef, deposit, now)
		}
	}
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, l, "permission", "dual_order",
				"请开通 binance 赚币/双币申购权限（%s腿下单失败：%v）", l.name, err)
		}
		return "", e.gap(ctx, l, "runtime", "dual_order",
			"系统侧病灶：venue 拒单（%s腿：%v）", l.name, err)
	}
	e.finish(ctx, oid, "filled", body)
	strike := 0.0
	if q.Strike != nil {
		strike = *q.Strike
	}
	note := fmt.Sprintf("DCI %s 申购回执：binance %s strike %.0f tenor %dd qty %.6f %s（单号 %s）",
		l.name, q.ProductRef, strike, tenor(q), qty, l.qtyCcy, reqID)
	opened := now
	asset := l.qtyCcy
	if _, err := e.Store.InsertPersonalPosition(ctx, asset, "other", asset+"USDT",
		note, &qty, &opened); err != nil {
		return "", fmt.Errorf("回执入账: %w", err)
	}
	if err := e.Store.InsertAlert(ctx, nil, "info", fmt.Sprintf(
		"DCI %s 申购成交：%.6f %s strike %.0f apy %.4g（单号 %s）",
		l.name, qty, l.qtyCcy, strike, q.APY, reqID)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s腿申购已提交并入账：%.6f %s strike %.0f（%.4g 原生口径）",
		l.name, qty, l.qtyCcy, strike, q.APY), nil
}
