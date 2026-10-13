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
	"errors"
	"fmt"
	"net/http"
	"nox/internal/risk/gate"
	"strings"
	"time"

	"nox/internal/store/pgstore"
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
	DCIOrderIntentCount(ctx context.Context, reqPrefix string) (int, error)
	DCIOrderOpenIntent(ctx context.Context, reqPrefix string) (bool, error)
	FinishDCIOrder(ctx context.Context, id int64, status, response string) error
	// 交割观察面。
	ListFilledDCIOrders(ctx context.Context) ([]pgstore.FilledDCIOrder, error)
	SettleDCIOrder(ctx context.Context, id int64, ccy string, qty, income float64) (bool, error)
	ClosePersonalPositionsByMarker(ctx context.Context, marker, suffix string) (int64, error)
	ExchangeConfigID(ctx context.Context, venue, venueType string) (int64, error)
	InsertIncomeEvents(ctx context.Context, evs []pgstore.IncomeEvent) (int64, error)
	InsertPersonalPosition(ctx context.Context, asset, kind, priceSymbol, note string, qty *float64, openedAt *time.Time) (int64, error)
	OutstandingDCIQty(ctx context.Context, venue, investCcy string) (float64, error)
	ListOpenPersonalPositions(ctx context.Context) ([]pgstore.PersonalPosition, error)
	UpdatePersonalPositionQtyNote(ctx context.Context, id int64, qty float64, note string) error
	OpenConditionGap(ctx context.Context, g *pgstore.ConditionGap) (int64, error)
	RequestConditionGap(ctx context.Context, id int64) error
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error
	// D-255：venue 败因坑表读/学面（tryLeg 申购前查坑、败行自动落坑）。
	ListVenueRejects(ctx context.Context, venue, op string) ([]pgstore.VenueReject, error)
	LearnVenueReject(ctx context.Context, venue, op, sig string, window time.Duration) error
	// 提案绑定面。
	ListProposals(ctx context.Context, status string, limit int) ([]pgstore.Proposal, error)
	LatestPendingProposal(ctx context.Context, templates []string) (*pgstore.Proposal, error)
	GetProposal(ctx context.Context, id int64) (*pgstore.Proposal, error)
	SetProposalStatus(ctx context.Context, id int64, status string) error
	// D-258 M4.1：评估卡消费面——无卡/非 viable=不算候选拒单；settle 回写实绩。
	GetOppEval(ctx context.Context, proposalID int64) (*pgstore.OppEval, error)
	UpdateOppEvalActual(ctx context.Context, proposalID int64, apr float64) error
	// D-260 M4.2：生命周期态读面——自动执行闸 state=='live' fail-closed。
	GetStrategyState(ctx context.Context, strategy string) (string, bool, error)
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
	PriceFn            func(ctx context.Context, symbol string) (float64, error)
	BalanceFn          func(ctx context.Context, asset string) (float64, error) // 现货 free+locked
	EarnFn             func(ctx context.Context, asset string) ([]EarnPosition, error)
	RedeemFn           func(ctx context.Context, productID string, amount float64) error
	StreamFn           func() bool                                                         // 流存活闸（nil=dephealth 实读）
	HoldingFn          func(ctx context.Context, productRef, optType string) (bool, error) // venue 持仓读（D-198 返修①；nil=实 GET）
	PositionsFn        func(ctx context.Context) ([]DCIPosition, error)                    // D-207 venue 持仓全列表（settle 观察；nil=实 GET）
	SettledPositionsFn func(ctx context.Context) ([]DCIPosition, error)                    // M3-④ venue SETTLED 终态行全列表（settle 死端切源；nil=实 GET）
}

// Go 裸口令主径：最新 pending 可执行提案→GoProposal 绑 id 执行（D-189③）。
func (e *Executor) Go(ctx context.Context) (string, error) {
	var tmpls []string
	for t := range gate.ExecTemplates {
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
	p, err := e.Store.GetProposal(ctx, id)
	if err != nil {
		return "", fmt.Errorf("提案#%d 查询败: %w", id, err)
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
	legs := gate.ExecTemplates[p.TemplateID]
	// R11② 执行租约：同 proposal_id 120s 内有单即拒（双击/重发防双成交）。
	if oid, err := e.Store.RecentDCIOrderByProposal(ctx, id, 120*time.Second); err != nil {
		return "", err
	} else if oid > 0 {
		return "", fmt.Errorf("提案#%d 刚执行过（订单#%d）拒单", id, oid)
	}
	if e.BnKey == "" {
		return "", e.gap(ctx, gate.CallLeg, "credential", "binance:spot",
			"请配置 binance 凭据（DCI 申购写径）")
	}
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return "", err
	}
	share, tenorCap := gate.TenorCapOf(cons)
	if share == nil {
		return "", e.gap(ctx, gate.CallLeg, "constraint", "custody_share",
			"请喂约束 custody_share（托管可动份额上限）")
	}
	spot, err := e.spotPrice(ctx, "BTCUSDT")
	if err != nil {
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, gate.CallLeg, "permission", "dual_quote",
				"binance BTC 现货价读取失败：%v", err)
		}
		return "", e.gap(ctx, gate.CallLeg, "runtime", "dual_quote",
			"系统侧病灶：BTC 现货价读取失败：%v", err)
	}
	var msgs []string
	var succeeded bool
	cb, pb := gate.OTMBoundRatios(cons)
	for _, l := range legs {
		ratio := l.RatioOf(cb, pb)
		if ratio == nil { // R9：界值行缺=constraint 缺口拒单（同 custody_share 例）
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.Name,
				e.gap(ctx, l, "constraint", l.BoundSubject(),
					"请喂约束 %s（%s腿 OTM 闸界比率）", l.BoundSubject(), l.Name)))
			continue
		}
		msg, err := e.tryLeg(ctx, l, *share, tenorCap, spot*(*ratio), spot, &p.ID)
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
