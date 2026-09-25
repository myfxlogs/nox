// service_graduation_sizing.go D-490 S2：毕业授权资金分配额折算器。
// 业主语义=「这个组合用多少资金」——funding_pct（小池净值占比，小数口径）或
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

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
)

// allocSizing 资金分配折算结果（写库值+互算回显+审计注记单源产出）。
type allocSizing struct {
	AllocUSD    float64  // 分配额 USD（pct 输入=minPool×pct；usd 输入=直填值）
	AllocPct    float64  // 占小池净值比（usd 输入的回显侧=allocUSD÷minPool；小数口径）
	GrossUSD    float64  // 双腿名义总闸 = AllocUSD（=max_gross_usd 落值）
	LegNotional float64  // 每腿名义 = AllocUSD÷2（L=1 最坏占用口径，落 notional_usd）
	LongQty     float64  // 买腿 qty 上限 = LegNotional÷perLotBuy(ask)×1.1（漂移余量）
	ShortQty    float64  // 卖腿 qty 上限（同式）
	MinPoolUSD  float64  // 绑定小池净值（note 审计标记用）
	MinPoolKey  string   // 绑定小池实际取到的 venue:type 键（如 "binance:perp"）
	Notes       []string // sizing_notes 回显注记（池口径/币种/快照时点）
}

// allocPool 腿资金池快照行（equity 原币+currency+fetched_at）。
type allocPool struct {
	key      string // 实际取到的 venue:venue_type 行键（unified 回退时如实反映口径）
	equity   float64
	currency string
	at       time.Time
}

// allocPoolOf 腿 venue_key→资金池快照行（SSOT §2 poolOf）：先精确 (venue,冒号后缀type)
// 行；缺行/NULL equity 回退 (venue,unified)→(venue,perp)→该 venue 任意非空 equity 行；
// 仍无=未采（false）。同所双池语义：spot×perp 组合两腿各取各自池行（现货花 spot 现金，
// 永续占 perp 保证金）——bare venue 折叠是漏行（D-358fix 同判）。
func allocPoolOf(snaps []pgstore.VenueAccountLatest, venueKey string) (allocPool, bool) {
	bare, vt := bareVenueKey(venueKey), ""
	if i := strings.Index(venueKey, ":"); i >= 0 {
		vt = venueKey[i+1:]
	}
	pick := func(wantType string) (allocPool, bool) {
		for _, r := range snaps {
			if r.Venue == bare && r.VenueType == wantType && r.EquityRaw != nil {
				return allocPool{key: bare + ":" + r.VenueType, equity: *r.EquityRaw, currency: r.Currency, at: r.FetchedAt}, true
			}
		}
		return allocPool{}, false
	}
	for _, t := range []string{vt, "unified", "perp"} {
		if p, ok := pick(t); ok {
			return p, true
		}
	}
	for _, r := range snaps {
		if r.Venue == bare && r.EquityRaw != nil {
			return allocPool{key: bare + ":" + r.VenueType, equity: *r.EquityRaw, currency: r.Currency, at: r.FetchedAt}, true
		}
	}
	return allocPool{}, false
}

// allocPoolUSDLike 池币种白名单（SSOT §3-4：UST/USDT→USD=1 既定口径，其他币种不猜汇率）。
func allocPoolUSDLike(ccy string) bool {
	switch strings.ToUpper(strings.TrimSpace(ccy)) {
	case "USD", "USDT", "UST":
		return true
	}
	return false
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
	// ② 双池快照：缺行/equity≤0/过期/币种——USD 直填也要池数据（安全线+互算回显都要）
	snaps, err := s.gradDB().LatestVenueAccountSnapshots(ctx)
	if err != nil {
		return res, fmt.Sprintf("账户快照读取失败: %v（fail-closed 不批钱）", err)
	}
	pools := map[string]allocPool{}
	for _, vk := range []string{venueBuy, venueSell} {
		p, ok := allocPoolOf(snaps, vk)
		if !ok {
			return res, fmt.Sprintf("腿池 %s 无资金快照（equity 未采集，fail-closed 不猜）", vk)
		}
		if p.equity <= 0 {
			return res, fmt.Sprintf("腿池 %s 净值非正（$%.2f），拒绝折算", p.key, p.equity)
		}
		if time.Since(p.at) > snapshotStaleWindow {
			return res, fmt.Sprintf("腿池 %s 快照过期（%s，>5min 未更新不猜）", p.key, p.at.Format("15:04:05"))
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
	// ③ 分配额归一 USD + 安全线闸（crossCap cfg 注入缺省 0.5，同待入场区档位判据）
	var allocUSD float64
	if pct != nil {
		allocUSD = minPool * *pct
	} else {
		allocUSD = *usd
		if allocUSD > minPool {
			return res, fmt.Sprintf("分配额 $%.0f 超过小池 %s 净值 $%.0f（必然超档，拒）", allocUSD, bind.key, minPool)
		}
	}
	allocPct := allocUSD / minPool // minPool>0 已由池闸保证
	cap := s.crossCap
	if cap <= 0 {
		cap = defaultCrossCap
	}
	if allocPct > cap {
		return res, fmt.Sprintf("占小池净值 %.1f%% 超安全线（上限 %.0f%%，同待入场区档位判据 ARB_CROSS_NOTIONAL_EQUITY_CAP；逃生=显式上限模式）", allocPct*100, cap*100)
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
		Notes: []string{
			fmt.Sprintf("买腿池 %s 净值 $%.0f %s（快照 %d 秒前）", poolBuy.key, poolBuy.equity, poolBuy.currency, int(time.Since(poolBuy.at).Seconds())),
			fmt.Sprintf("卖腿池 %s 净值 $%.0f %s（快照 %d 秒前）", poolSell.key, poolSell.equity, poolSell.currency, int(time.Since(poolSell.at).Seconds())),
			fmt.Sprintf("绑定约束=小池 %s $%.0f（双池取 min）", bind.key, minPool),
		},
	}
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
	resp.Ok = true
}

// allocNoteMarker D-490 §4：decided_note 审计标记——pct 侧「分配$X(=P%×净值池$Ntype)」、
// usd 侧「分配$X(直填=P%×净值池$N)」；业主 note 保留在前（本函数返回拼接后整串）。
func allocNoteMarker(userNote string, res allocSizing, pctInput bool) string {
	poolType := res.MinPoolKey
	if i := strings.Index(poolType, ":"); i >= 0 {
		poolType = poolType[i+1:]
	}
	marker := fmt.Sprintf("分配$%.1f(直填=%.1f%%×净值池$%.0f)", res.AllocUSD, res.AllocPct*100, res.MinPoolUSD)
	if pctInput {
		marker = fmt.Sprintf("分配$%.1f(=%.1f%%×净值池$%.0f%s)", res.AllocUSD, res.AllocPct*100, res.MinPoolUSD, poolType)
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
