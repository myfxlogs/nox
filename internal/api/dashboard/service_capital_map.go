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
	"arb/internal/ledger/reconcile"

	"connectrpc.com/connect"
)

// capitalGapTTL 三缺口实拉缓存期（业主口径「页面开即实拉+60s 进程缓存」）。
const capitalGapTTL = 60 * time.Second

// CapitalGapFetcher 三缺口实拉通道（reconcile.BinanceCapitalGaps 实现；测试注入计数 fake）。
type CapitalGapFetcher interface {
	Fetch(ctx context.Context) (*reconcile.CapitalGaps, error)
}

// capitalMapCaliberNote 口径注记（面板尾注）。D-125：域词中文化；D-130 域2：binance 资金钱包面
// 官方已废并入现货（六面→五面）、面名随 venue 原生词；D-133 R1：binance 理财面整体摘除
// （业主令：理财账户实有杂币不纳入）→binance 余 现货/合约（U本位）/币本位/期权 四面。
const capitalMapCaliberNote = "口径：binance 四面全景（现货/合约/U 本位/币本位/期权；资金钱包面官方已废并入现货·D-130、理财面摘除·D-133）+okx 三面（资金账户/交易账户/赚币）+gate 并读（现货/合约/理财）×三档可用性——立即可用=活账本/快照既有流（与 sizing 基准同源，sizing 实取各池最小值，全景不改基准）；可转可用=现货锁定（净值−可用 推得）+okx 资金账户/赚币；锁定慢动=币本位+期权权益+okx 占用冻结+gate 现货锁定/交割保证金/理财出借。USDT/LDUSDT 1:1 同视 USD；杂币经 venue 指数价/tick 中间价折算，缺价如实标「未折算」不猜。缺口/可用性面=页面开实拉+60s 进程缓存；采集失败/未配置/权限缺席=节点如实标缺不猜零。"

// GetCapitalMap D-546：三档全景树（后端算，前端零计算）。
func (s *Service) GetCapitalMap(ctx context.Context, req *connect.Request[v1.GetCapitalMapRequest]) (*connect.Response[v1.GetCapitalMapResponse], error) {
	s.kickNostreamAccounts() // 3d-P1#1c：无流子账户（无 WS 余额频道）按需回读，10s/键防抖
	resp := s.buildCapitalMap(ctx)
	resp.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	resp.CaliberNote = capitalMapCaliberNote
	return connect.NewResponse(resp), nil
}

// buildCapitalMap 六面节点 → 三档归类（立即可用/可转可用/锁定慢动）。
func (s *Service) buildCapitalMap(ctx context.Context) *v1.GetCapitalMapResponse {
	snaps, err := s.liveAccountLatest(ctx)
	if err != nil {
		log.Printf("[capital-map] snapshots: %v（快照面如实标缺）", err)
		snaps = nil
	}
	gaps := s.capitalGaps(ctx)

	resp := &v1.GetCapitalMapResponse{}
	avail := &v1.CapitalMapTier{Tier: "立即可用", Note: "=sizing 基准口径（sizing 实取各池最小值，全景不改基准）"}
	transfer := &v1.CapitalMapTier{Tier: "可转可用"}
	locked := &v1.CapitalMapTier{Tier: "锁定/慢动"}

	// ① 现货可用（活账本流：ws→REST 校准→快照，D-542 同径）。D-125：节点标签域词中文化
	// （venue·类型 状态，循池标签 `okx·统一账户` 式；标签为纯展示值，键面 venue:type 不动）。
	spot, spotOK := s.legPool(ctx, snaps, "binance:spot")
	if spotOK && spot.free != nil {
		avail.Nodes = append(avail.Nodes, capNode("binance·现货 可用", *spot.free, spot.currency, capSrcNote(spot), spot.at))
	} else {
		avail.Nodes = append(avail.Nodes, capMissing("binance·现货 可用", "活账本/校准/快照三径全空"))
	}
	// ② U 本位可用（同径）。
	perp, perpOK := s.legPool(ctx, snaps, "binance:perp")
	if perpOK && perp.free != nil {
		avail.Nodes = append(avail.Nodes, capNode("binance·合约 可用", *perp.free, perp.currency, capSrcNote(perp), perp.at))
	} else {
		avail.Nodes = append(avail.Nodes, capMissing("binance·合约 可用", "活账本/校准/快照三径全空"))
	}
	// ④ 现货锁定（可转档）：账本 Equity−Available 推得（WS eq=free+locked / REST 同约）。
	if e, ok := s.ledgerSpot(); ok {
		lockedAmt := e.Equity - e.Available
		n := capNode("binance·现货 锁定", lockedAmt, e.Currency, "账本净值−可用 推得（源="+srcCn(e.Src)+"）", e.At)
		transfer.Nodes = append(transfer.Nodes, n)
	} else {
		transfer.Nodes = append(transfer.Nodes, capMissing("binance·现货 锁定", "活账本空——快照口径无可用/锁定分列"))
	}
	// ⑤ 币本位（锁定档，dapi 逐币行；面败=标缺，面正常零行=诚实零节点）。
	switch {
	case gaps == nil:
		locked.Nodes = append(locked.Nodes, capMissing("binance 币本位", "未配置 binance 实盘凭据"))
	case faceFailed(gaps, "币本位面"):
		locked.Nodes = append(locked.Nodes, capMissing("binance 币本位", "实拉失败（"+faceMsg(gaps, "币本位面")+"）"))
	default:
		locked.Nodes = append(locked.Nodes, capCoinOptNodes(ctx, s, "binance 币本位 ", "dapi/v1/balance", gaps.FetchedAt, coinmRows(gaps))...)
	}
	// ⑥ 期权权益（锁定档，eapi 逐币行；只取权益额不收 greeks）。
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

	// ⑨ okx 立即可用=交易账户可用（活账本 okx:unified 优先，REST 回退）。
	avail.Nodes = append(avail.Nodes, s.availLiveNode(ctx, snaps, "okx:unified", "okx·交易账户 可用", availWallet(okxVA, "trading"), "/api/v5/account/balance"))
	// ⑩ okx 锁定慢动=交易账户占用/冻结（availBal−availEq+frozenBal）。
	locked.Nodes = append(locked.Nodes, walletLocked(okxVA, "trading", "okx·交易账户 占用/冻结", "/api/v5/account/balance"))
	// ⑪ okx 可转可用=资金钱包+活期理财（资产↔交易划转 T+0）。
	transfer.Nodes = append(transfer.Nodes, walletTransfer(okxVA, "funding", "okx 资金账户", "/api/v5/asset/balances"))
	transfer.Nodes = append(transfer.Nodes, walletTransfer(okxVA, "earn", "okx 赚币", "/api/v5/finance/savings/balance"))

	// ⑫ gate 立即可用=合约可用（活账本 gate:perp 优先）+现货可用（REST）。
	avail.Nodes = append(avail.Nodes, s.availLiveNode(ctx, snaps, "gate:perp", "gate·合约 可用", availWallet(gateVA, "futures"), "/api/v4/futures/usdt/accounts"))
	avail.Nodes = append(avail.Nodes, s.availLiveNode(ctx, snaps, "", "gate·现货 可用", availWallet(gateVA, "spot"), "/api/v4/spot/accounts"))
	// ⑬ gate 锁定慢动=现货锁定+合约保证金占用+理财出借中。
	locked.Nodes = append(locked.Nodes, walletLocked(gateVA, "spot", "gate·现货 锁定", "/api/v4/spot/accounts"))
	locked.Nodes = append(locked.Nodes, walletLocked(gateVA, "futures", "gate·交割 保证金占用", "/api/v4/futures/usdt/accounts"))
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
