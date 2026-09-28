// service_capital_map.go —— D-546 S2：资金全景账三档树 RPC（binance 六面钱包）。
// 口径：立即可用档=活账本/快照既有流只读（与 sizing 基准同源，全景不进基准——A3 隔离锚）；
// 可转/锁定档=三缺口 REST 实拉（60s 进程缓存，页面开即实拉）；缺价币如实标「未折算」不猜；
// 采集失败/缺价=degraded 如实降级，节点仍出（六面全出），不炸 RPC。
package dashboard

import (
	"context"
	"log"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/reconcile"

	"connectrpc.com/connect"
)

// capitalGapTTL 三缺口实拉缓存期（业主口径「页面开即实拉+60s 进程缓存」）。
const capitalGapTTL = 60 * time.Second

// CapitalGapFetcher 三缺口实拉通道（reconcile.BinanceCapitalGaps 实现；测试注入计数 fake）。
type CapitalGapFetcher interface {
	Fetch(ctx context.Context) (*reconcile.CapitalGaps, error)
}

// capitalMapCaliberNote 口径注记（面板尾注）。
const capitalMapCaliberNote = "口径：binance 六面全景（现货/资金钱包/U 本位/币本位/期权/理财）+okx 统一账户三面（trading/funding/earn）+gate 两面并读（spot/futures/earn）×三档可用性——立即可用=活账本/快照既有流（与 sizing 基准同源，sizing 实取各池最小值，全景不改基准）；可转可用=理财活期（T+0 赎回）+现货 locked（Equity−Available 推得）+okx 资金钱包/活期理财；锁定慢动=理财定期+币本位+期权权益+okx 占用冻结+gate spot locked/futures 保证金/earn 出借。USDT/LDUSDT 1:1 同视 USD；杂币经 venue 指数价/tick mid 折算，缺价如实标「未折算」不猜。gap/availability 面=页面开实拉+60s 进程缓存；采集失败/未配置/权限缺席=节点如实标缺不猜零。"

// GetCapitalMap D-546：三档全景树（后端算，前端零计算）。
func (s *Service) GetCapitalMap(ctx context.Context, req *connect.Request[v1.GetCapitalMapRequest]) (*connect.Response[v1.GetCapitalMapResponse], error) {
	resp := s.buildCapitalMap(ctx)
	resp.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	resp.CaliberNote = capitalMapCaliberNote
	return connect.NewResponse(resp), nil
}

// buildCapitalMap 六面节点 → 三档归类（立即可用/可转可用/锁定慢动）。
func (s *Service) buildCapitalMap(ctx context.Context) *v1.GetCapitalMapResponse {
	snaps, err := s.store.LatestVenueAccountSnapshots(ctx)
	if err != nil {
		log.Printf("[capital-map] snapshots: %v（快照面如实标缺）", err)
		snaps = nil
	}
	gaps := s.capitalGaps(ctx)

	resp := &v1.GetCapitalMapResponse{}
	avail := &v1.CapitalMapTier{Tier: "立即可用", Note: "=sizing 基准口径（sizing 实取各池最小值，全景不改基准）"}
	transfer := &v1.CapitalMapTier{Tier: "可转可用"}
	locked := &v1.CapitalMapTier{Tier: "锁定/慢动"}

	// ① 现货 free（活账本流：ws→REST 校准→快照，D-542 同径）。
	spot, spotOK := s.legPool(ctx, snaps, "binance:spot")
	if spotOK && spot.free != nil {
		avail.Nodes = append(avail.Nodes, capNode("binance:spot free", *spot.free, spot.currency, capSrcNote(spot), spot.at))
	} else {
		avail.Nodes = append(avail.Nodes, capMissing("binance:spot free", "活账本/校准/快照三径全空"))
	}
	// ② U 本位 available（同径）。
	perp, perpOK := s.legPool(ctx, snaps, "binance:perp")
	if perpOK && perp.free != nil {
		avail.Nodes = append(avail.Nodes, capNode("binance:perp available", *perp.free, perp.currency, capSrcNote(perp), perp.at))
	} else {
		avail.Nodes = append(avail.Nodes, capMissing("binance:perp available", "活账本/校准/快照三径全空"))
	}
	// ③ 资金钱包（已有池=调度快照；funding 无私有流，恒快照口径）。
	avail.Nodes = append(avail.Nodes, s.fundingNode(snaps))

	// ④ 现货 locked（可转档）：账本 Equity−Available 推得（WS eq=free+locked / REST 同约）。
	if e, ok := s.ledgerSpot(); ok {
		lockedAmt := e.Equity - e.Available
		n := capNode("binance:spot locked", lockedAmt, e.Currency, "账本 Equity−Available 推得（src="+e.Src+"）", e.At)
		transfer.Nodes = append(transfer.Nodes, n)
	} else {
		transfer.Nodes = append(transfer.Nodes, capMissing("binance:spot locked", "活账本空——快照口径无 free/locked 分列"))
	}
	// ⑤ 理财活期（可转档，T+0）。
	if gaps == nil || faceFailed(gaps, "理财面") {
		transfer.Nodes = append(transfer.Nodes, capEarnMissing(gaps, "binance 理财活期"))
	} else {
		transfer.Nodes = append(transfer.Nodes, capEarnNode(gaps, "flexible"))
	}

	// ⑥ 理财定期（锁定档，到期赎回）。
	if gaps == nil || faceFailed(gaps, "理财面") {
		locked.Nodes = append(locked.Nodes, capEarnMissing(gaps, "binance 理财定期"))
	} else {
		locked.Nodes = append(locked.Nodes, capEarnNode(gaps, "locked"))
	}
	// ⑦ 币本位（锁定档，dapi 逐币行；面败=标缺，面正常零行=诚实零节点）。
	switch {
	case gaps == nil:
		locked.Nodes = append(locked.Nodes, capMissing("binance 币本位", "未配置 binance 实盘凭据"))
	case faceFailed(gaps, "币本位面"):
		locked.Nodes = append(locked.Nodes, capMissing("binance 币本位", "实拉失败（"+faceMsg(gaps, "币本位面")+"）"))
	default:
		locked.Nodes = append(locked.Nodes, capCoinOptNodes(ctx, s, "binance 币本位 ", "dapi/v1/balance", gaps.FetchedAt, coinmRows(gaps))...)
	}
	// ⑧ 期权权益（锁定档，eapi 逐币行；只取权益额不收 greeks）。
	switch {
	case gaps == nil:
		locked.Nodes = append(locked.Nodes, capMissing("binance 期权权益", "未配置 binance 实盘凭据"))
	case faceFailed(gaps, "期权面"):
		locked.Nodes = append(locked.Nodes, capMissing("binance 期权权益", "实拉失败（"+faceMsg(gaps, "期权面")+"）"))
	default:
		locked.Nodes = append(locked.Nodes, capCoinOptNodes(ctx, s, "binance 期权权益 ", "eapi/v1/account", gaps.FetchedAt, optionsRows(gaps))...)
	}

	// ===== D-546 R1 扩面：okx（unified 子账本拆分）+gate（spot/futures 并读）=====
	okxVA := s.venueAvailability(ctx, "okx")
	gateVA := s.venueAvailability(ctx, "gate")

	// ⑨ okx 立即可用=统一账户 trading available（活账本 okx:unified 优先，REST 回退）。
	var okxAvail float64
	var okxAt time.Time
	if tr := availWallet(okxVA, "trading"); tr != nil {
		okxAvail, okxAt = tr.AvailableUSD, tr.At
	}
	avail.Nodes = append(avail.Nodes, s.availLiveNode(ctx, snaps, "okx:unified", "okx:unified available", okxAvail, okxAt))
	// ⑩ okx 锁定慢动=trading 占用/冻结（availBal−availEq+frozenBal）。
	locked.Nodes = append(locked.Nodes, walletLocked(okxVA, "trading", "okx:unified 占用/冻结", "/api/v5/account/balance"))
	// ⑪ okx 可转可用=资金钱包+活期理财（资产↔交易划转 T+0）。
	transfer.Nodes = append(transfer.Nodes, walletTransfer(okxVA, "funding", "okx 资金钱包", "/api/v5/asset/balances"))
	transfer.Nodes = append(transfer.Nodes, walletTransfer(okxVA, "earn", "okx 活期理财", "/api/v5/finance/savings/balance"))

	// ⑫ gate 立即可用=futures available（活账本 gate:perp 优先）+spot available（REST）。
	var gfAvail, gsAvail float64
	var gfAt, gsAt time.Time
	if fu := availWallet(gateVA, "futures"); fu != nil {
		gfAvail, gfAt = fu.AvailableUSD, fu.At
	}
	if sp := availWallet(gateVA, "spot"); sp != nil {
		gsAvail, gsAt = sp.AvailableUSD, sp.At
	}
	avail.Nodes = append(avail.Nodes, s.availLiveNode(ctx, snaps, "gate:perp", "gate:perp available", gfAvail, gfAt))
	avail.Nodes = append(avail.Nodes, s.availLiveNode(ctx, snaps, "", "gate:spot available", gsAvail, gsAt))
	// ⑬ gate 锁定慢动=spot locked+futures 保证金占用+earn 出借中。
	locked.Nodes = append(locked.Nodes, walletLocked(gateVA, "spot", "gate:spot locked", "/api/v4/spot/accounts"))
	locked.Nodes = append(locked.Nodes, walletLocked(gateVA, "futures", "gate:futures 保证金占用", "/api/v4/futures/usdt/accounts"))
	locked.Nodes = append(locked.Nodes, walletLocked(gateVA, "earn", "gate 理财出借中", "/api/v4/earn/uni/lends"))

	capTierSum(avail)
	capTierSum(transfer)
	capTierSum(locked)
	resp.Tiers = []*v1.CapitalMapTier{avail, transfer, locked}
	for _, t := range resp.Tiers {
		for _, n := range t.Nodes {
			if !n.HasUsd {
				resp.Degraded = true
			}
		}
	}
	return resp
}
