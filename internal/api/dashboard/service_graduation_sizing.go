// service_graduation_sizing.go D-490 S2：毕业授权资金分配额折算器。
// 业主语义=「这个组合用多少资金」——funding_pct（可用基准占比，小数口径，D-540 业主令改）或
// funding_usd（直填金额）由服务端折算成四字段绝对值（max_long_qty/max_short_qty/
// max_gross_usd/notional_usd），approve 快照固化写库，引擎/schema 零改动。
// fail-closed 链（SSOT §3）：互斥 → 池缺/equity≤0/快照>5min/币种非USD系 →
// 安全线（allocUSD÷minPool>crossCap）→ perLot 缺 → 双腿 qty<minStep 死额度。
package dashboard

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"nox/internal/data/keydomain"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// allocSizing 资金分配折算结果（写库值+互算回显+审计注记单源产出）。
type allocSizing struct {
	AllocUSD    float64  // 分配额 USD（pct 输入=base×pct 可用基准口径；usd 输入=直填值）
	AllocPct    float64  // 占可用基准比（D-540：allocUSD÷base；回显口径，小数）
	GrossUSD    float64  // 双腿名义总闸 = AllocUSD（=max_gross_usd 落值）
	LegNotional float64  // 每腿名义 = AllocUSD÷2（L=1 最坏占用口径，落 notional_usd）
	LongQty     float64  // 买腿 qty 上限 = LegNotional÷perLotBuy(ask)×1.1（漂移余量）
	ShortQty    float64  // 卖腿 qty 上限（同式）
	MinPoolUSD  float64  // 绑定小池净值（note 审计标记用）
	MinPoolKey  string   // 绑定小池实际取到的 venue:type 键（如 "binance:perp"）
	Notes       []string // sizing_notes 回显注记（池口径/币种/快照时点）
	// D-540：可用基准+风险位+建议额（栏内联动/红字数据源）。
	AllocBaseUSD   float64  // 可用基准=min(双池可用)；缺 free_margin 回退 equity
	AllocBaseNote  string   // 基准来源注记（free_margin/净值回退 逐池标）
	AllocBaseKey   string   // 基准绑定池键（note 审计标记用）
	RiskLevel      string   // ok / insufficient / liquidation
	SuggestedAlloc float64  // 建议分配额（risk≠ok 时给）
	RiskReasons    []string // 风险理由逐项
}

// deriveAllocSizing D-490 S3 共用折算器（approveNonCfd+cfd 两分支同源）：
// 互斥校验 → poolOf 双池 min → 币种/5min 新鲜度/crossCap 安全线闸 →
// qty=alloc÷2÷perLot×1.1 → 双腿 QtyBelowMinStep 预检（MT 腿可在任一侧——
// short_cfd 卖腿=MT，两腿都查才无死额度缺口；CEX 腿无 step 概念天然跳过）。
// 返回 errStr 非空 = fail-closed 拒批文案（in-band，前端原样展示）。
func (s *Service) deriveAllocSizing(ctx context.Context, venueBuy, venueSell, canonBuy, canonSell string, pct, usd *float64) (allocSizing, string) {
	var res allocSizing
	// ① 互斥校验（显式四字段混传由调用方拦——本方只见 pct/usd 两侧）
	if pct != nil && usd != nil {
		return res, "funding_pct 与 funding_usd 互斥（分配额二选一：填 % 或填 $）"
	}
	if pct == nil && usd == nil {
		return res, "资金分配模式缺输入（funding_pct/funding_usd 至少传一，或走显式上限）"
	}
	if pct != nil && !(*pct > 0) {
		return res, "funding_pct 必须 >0（NaN/非正同拒）"
	}
	if usd != nil && !(*usd > 0) {
		return res, "funding_usd 必须 >0（NaN/非正同拒）"
	}
	// ② 双池求值：D-565 S4 批钱面全实时——CEX 腿=活账本/决策时刻 REST 实拉
	// （LiveFresh 绕 restTTL，快照兜底删）；MT 腿=AccountSummary 实时 RPC（快照
	// 退批钱面，展示面照旧）。两径双败/equity≤0/币种非 USD 系 → fail-closed 不批。
	pools := map[string]allocPool{}
	for _, vk := range []string{venueBuy, venueSell} {
		p, ok, failNote := s.legPoolSizing(ctx, vk)
		if !ok {
			return res, fmt.Sprintf("腿池 %s %s（fail-closed 不批钱）", vk, failNote)
		}
		if p.equity <= 0 {
			return res, fmt.Sprintf("腿池 %s 净值非正（$%.2f），拒绝折算", p.key, p.equity)
		}
		if !allocPoolUSDLike(p.currency) {
			return res, fmt.Sprintf("腿池 %s 币种 %s 不支持（仅 USD/USDT/UST 不猜汇率；逃生=显式上限模式）", p.key, p.currency)
		}
		pools[vk] = p
	}
	poolBuy, poolSell := pools[venueBuy], pools[venueSell]
	minPool := math.Min(poolBuy.equity, poolSell.equity)
	bind := poolBuy
	if poolSell.equity < poolBuy.equity {
		bind = poolSell
	}
	// D-540：可用资金基准（min 双池可用，free_margin 优先缺省回退净值）——业主令 % 基准。
	base, baseBindKey, baseNote := allocBaseEval(poolBuy, poolSell)
	// ③ 分配额归一 USD + 安全线闸（crossCap cfg 注入缺省 0.5，同待入场区档位判据）
	var allocUSD float64
	if pct != nil {
		if !(base > 0) {
			return res, "可用基准非正（free_margin 未采/为零），%% 口径不可折算——直填 $ 或等快照"
		}
		allocUSD = base * *pct // D-540：% 基准=可用资金（业主令；旧净值口径随动）
	} else {
		allocUSD = *usd
		if allocUSD > minPool {
			return res, fmt.Sprintf("分配额 $%.0f 超过小池 %s 净值 $%.0f（必然超档，拒）", allocUSD, bind.key, minPool)
		}
	}
	allocPct := 0.0
	if base > 0 {
		allocPct = allocUSD / base // D-540：回显侧同基准（占可用资金比，小数口径）
	}
	cap := s.crossCap
	if cap <= 0 {
		cap = defaultCrossCap
	}
	// 安全线闸口径不变——占小池净值比（equity 口径；与回显的可用基准比分轨）。
	if equityPct := allocUSD / minPool; equityPct > cap {
		return res, fmt.Sprintf("占小池净值 %.1f%% 超安全线（上限 %.0f%%，同待入场区档位判据 NOX_CROSS_NOTIONAL_EQUITY_CAP；逃生=显式上限模式）", equityPct*100, cap*100)
	}
	// ④ perLot 折算（USDPerLotForLeg 复用；报价/CS/FX 缺 → fail-closed）
	notional := allocUSD / 2 // L=1 最坏占用：两腿资金占用上界≈2×腿名义
	perLotBuy, okB := s.USDPerLotForLeg(canonBuy, venueBuy, "ask")
	if !okB || perLotBuy <= 0 {
		return res, fmt.Sprintf("买腿 %s %s 每手名义不可得（报价/合约规格/FX 缺，fail-closed 不猜）", venueBuy, canonBuy)
	}
	perLotSell, okS := s.USDPerLotForLeg(canonSell, venueSell, "ask")
	if !okS || perLotSell <= 0 {
		return res, fmt.Sprintf("卖腿 %s %s 每手名义不可得（报价/合约规格/FX 缺，fail-closed 不猜）", venueSell, canonSell)
	}
	// ⑤ 双腿 minStep 预检（QtyBelowMinStep 复用；不许授权永远开不了仓的死额度。
	// venueSymbol 复用 perLot 已命中的同键报价——quote 缺失在此不可达）。
	qtyBuy, qtySell := notional/perLotBuy, notional/perLotSell
	legSym := func(canon, venueKey string) string {
		if q, ok := s.qbus.Latest(canon, venueKey); ok {
			return q.VenueSymbol
		}
		return ""
	}
	if s.QtyBelowMinStep(venueBuy, legSym(canonBuy, venueBuy), qtyBuy) {
		return res, fmt.Sprintf("分配额折算后买腿 qty %.4f 低于 %s 最小下单单位（死额度不批；调大分配额或走显式上限）", qtyBuy, venueBuy)
	}
	if s.QtyBelowMinStep(venueSell, legSym(canonSell, venueSell), qtySell) {
		return res, fmt.Sprintf("分配额折算后卖腿 qty %.4f 低于 %s 最小下单单位（死额度不批；调大分配额或走显式上限）", qtySell, venueSell)
	}
	res = allocSizing{
		AllocUSD: allocUSD, AllocPct: allocPct,
		GrossUSD: allocUSD, LegNotional: notional,
		LongQty: qtyBuy * 1.1, ShortQty: qtySell * 1.1,
		MinPoolUSD: minPool, MinPoolKey: bind.key,
		AllocBaseUSD:  base,
		AllocBaseNote: baseNote,
		AllocBaseKey:  baseBindKey,
		Notes: []string{
			fmt.Sprintf("买腿池 %s 净值 $%.0f %s（%s）", poolBuy.key, poolBuy.equity, poolBuy.currency, poolSrcNote(poolBuy)),
			fmt.Sprintf("卖腿池 %s 净值 $%.0f %s（%s）", poolSell.key, poolSell.equity, poolSell.currency, poolSrcNote(poolSell)),
			fmt.Sprintf("绑定约束=小池 %s $%.0f（双池取 min）", bind.key, minPool),
			fmt.Sprintf("可用基准 $%.0f（%s）", base, baseNote),
		},
	}
	res.RiskLevel, res.RiskReasons, res.SuggestedAlloc = s.allocRisk(ctx, res, venueBuy, venueSell, canonBuy, canonSell, poolBuy, poolSell)
	return res, ""
}

// allocModeInputs D-490 模式分流校验（approve 入口共用）：funding=资金分配模式
// （pct/usd 任一非 nil）。互斥①：资金输入与显式四字段混传拒；互斥②：显式模式
// caps 必填>0（禁默认=防盲签；!>0 写法一并拒 NaN）、notional 显式传禁非正
// （D-367 加载期剔除=假授权）。errStr 非空=fail-closed 拒批文案。
func allocModeInputs(g *v1.ApproveCarryGraduationRequest) (funding bool, errStr string) {
	funding = g.FundingPct != nil || g.FundingUsd != nil
	explicit := g.MaxLongQty > 0 || g.MaxShortQty > 0 || g.MaxGrossUsd > 0 || g.NotionalUsd != nil
	if funding && explicit {
		return true, "资金分配模式与显式上限互斥（funding_pct/funding_usd 不得与 max_*_qty/max_gross_usd/notional_usd 混传）"
	}
	if !funding && !explicit && g.DryRun {
		return false, "" // D-492 R1：dry_run 裸探针豁免——无 sizing/显式入参放行回 gap 体检字段（零副作用）
	}
	if !funding {
		if !(g.MaxLongQty > 0) || !(g.MaxShortQty > 0) || !(g.MaxGrossUsd > 0) {
			return false, "三 cap 必填且 >0（max_long_qty/max_short_qty/max_gross_usd，跟 venue_buy/venue_sell 腿走）；或改用资金分配（funding_pct/funding_usd）"
		}
		if g.NotionalUsd != nil && !(*g.NotionalUsd > 0) {
			return false, "notional_usd 显式传值禁非正（D-367 加载期会整行剔除=假授权）"
		}
	}
	return funding, ""
}

// allocApply 资金分配折算+回填三产（派生 caps / est_* 回显 / note 审计标记）——
// cfd 与 spot_carry 两分支共用同一助手，禁止各自抄一遍（P3 单源）。
func (s *Service) allocApply(ctx context.Context, resp *v1.ApproveCarryGraduationResponse, venueBuy, venueSell, canonBuy, canonSell string, g *v1.ApproveCarryGraduationRequest, note string) (longQty, shortQty, grossUSD float64, notionalUSD *float64, userNote string, estr string) {
	res, estr := s.deriveAllocSizing(ctx, venueBuy, venueSell, canonBuy, canonSell, g.FundingPct, g.FundingUsd)
	if estr != "" {
		return 0, 0, 0, nil, note, estr
	}
	fillSizingEst(resp, res)
	return res.LongQty, res.ShortQty, res.GrossUSD, &res.LegNotional, allocNoteMarker(note, res, g.FundingPct != nil), ""
}

// gradDryRunResp dry_run 共用回显组装：venue gaps + 敞口闸 warns + est_* ——纯响应
// 组装零副作用（不写库/不落账/不热重载；前置校验已拒绝一切写路径依赖）。
func (s *Service) gradDryRunResp(ctx context.Context, resp *v1.ApproveCarryGraduationResponse, venueBuy, venueSell string, capWarns []string, funding bool, longQty, shortQty, gross float64, notional *float64) {
	resp.VenueGapKeys = venueGapsOf(venueBuy, venueSell, s.allowVenuesNow(ctx)) // D-492：纯 key 列表供补开钮
	resp.VenueGaps = append(resp.VenueGapKeys, capWarns...)
	if !funding {
		fillExplicitEst(resp, longQty, shortQty, gross, notional)
	}
	// D-508 S8 + D-542：腿→资金池映射随 dry_run 回传——CEX 腿先走实拉活账本
	// （裸探针=弹窗开，脏/空触发一发 REST 校准+30s 缓存）；REST 双败快照兜底。
	// 快照读失败→空列表（前端如实显「未采」降级）。
	if snaps, err := s.liveAccountLatest(ctx); err == nil {
		var poolB, poolS allocPool
		var okB, okS bool
		resp.LegPools, poolB, okB, poolS, okS = s.gradLiveLegPools(ctx, snaps, []string{venueBuy, venueSell})
		// D-540：可用基准随 dry_run 常回（裸探针无 sizing 入参也要——前端栏内联动基准；
		// 池缺/币种不符→不回传，前端如实显「未采」）。
		if okB && okS && allocPoolUSDLike(poolB.currency) && allocPoolUSDLike(poolS.currency) {
			b, _, n := allocBaseEval(poolB, poolS)
			resp.AllocBaseUsd, resp.AllocBaseNote = b, n
		}
	}
	resp.Ok = true
}

// gradLiveLegPools 双腿池行组装（D-542：gradLegPools 实拉版——同回退链但 CEX 腿先活账本）。
func (s *Service) gradLiveLegPools(ctx context.Context, snaps []pgstore.VenueAccountLatest, legKeys []string) (rows []*v1.GradLegPoolRow, poolB allocPool, okB bool, poolS allocPool, okS bool) {
	pools := make([]allocPool, len(legKeys))
	oks := make([]bool, len(legKeys))
	for i, vk := range legKeys {
		pools[i], oks[i] = s.legPool(ctx, snaps, vk)
	}
	rows = gradLegPoolsOf(legKeys, pools, oks)
	if len(pools) > 0 {
		poolB, okB = pools[0], oks[0]
	}
	if len(pools) > 1 {
		poolS, okS = pools[1], oks[1]
	}
	return rows, poolB, okB, poolS, okS
}

// poolSrcNote 池来源注记（sizing_notes 透出——实拉/校准 vs 快照龄）。
func poolSrcNote(p allocPool) string {
	if p.live {
		if p.src == "rest" {
			return fmt.Sprintf("实拉校准 %d 秒前", int(time.Since(p.at).Seconds()))
		}
		return "实拉"
	}
	return fmt.Sprintf("快照 %d 秒前（快照兜底）", int(time.Since(p.at).Seconds()))
}

// allocNoteMarker D-490 §4：decided_note 审计标记——pct 侧「分配$X(=P%×可用基准$Ntype)」、
// usd 侧「分配$X(直填=P%×可用基准$N)」；业主 note 保留在前（本函数返回拼接后整串）。
// D-540：基准=可用资金（free_margin 优先缺省回退净值），标记同口径不残留净值字样。
func allocNoteMarker(userNote string, res allocSizing, pctInput bool) string {
	poolType := res.AllocBaseKey
	if t := keydomain.Parse(poolType).Type; t != "" {
		poolType = t
	}
	marker := fmt.Sprintf("分配$%.1f(直填=%.1f%%×可用基准$%.0f)", res.AllocUSD, res.AllocPct*100, res.AllocBaseUSD)
	if pctInput {
		marker = fmt.Sprintf("分配$%.1f(=%.1f%%×可用基准$%.0f%s)", res.AllocUSD, res.AllocPct*100, res.AllocBaseUSD, poolType)
	}
	return strings.TrimSpace(userNote + " " + marker)
}

// fillSizingEst D-490：折算结果回显 proto 字段（dry_run 试算与真批准同径透出）。
func fillSizingEst(resp *v1.ApproveCarryGraduationResponse, res allocSizing) {
	resp.EstGrossUsd = res.GrossUSD
	resp.EstLegNotionalUsd = res.LegNotional
	resp.EstLongQty = res.LongQty
	resp.EstShortQty = res.ShortQty
	resp.EstAllocUsd = res.AllocUSD
	resp.EstAllocPct = res.AllocPct
	resp.SizingNotes = res.Notes
	// D-540：可用基准+风险位+建议额随试算/批准同径透出。
	resp.AllocBaseUsd = res.AllocBaseUSD
	resp.AllocBaseNote = res.AllocBaseNote
	resp.RiskLevel = res.RiskLevel
	resp.SuggestedAllocUsd = res.SuggestedAlloc
	resp.RiskReasons = res.RiskReasons
}

// fillExplicitEst 显式模式 dry_run 回显（无折算——回显业主将写入的原值；
// alloc 字段留零=无池分配语义）。
func fillExplicitEst(resp *v1.ApproveCarryGraduationResponse, longQty, shortQty, gross float64, notional *float64) {
	resp.EstGrossUsd = gross
	resp.EstLongQty = longQty
	resp.EstShortQty = shortQty
	if notional != nil {
		resp.EstLegNotionalUsd = *notional
	}
}
