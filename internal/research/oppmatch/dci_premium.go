// dci_premium.go —— D-514 DCI 溢价闸：dci_buy_low 权利金年化 < max(3×earn_apr, 8%)
// →「无达标档」不产提案。premium_ann=fin_cex_dci_quote 最优 PUT 档 apy（venue
// 原生年化口径——权利金/本金×365/tenor venue 已算）；earn_apr=fin_cex_earn_apr
// 现值（MaxEarnAPR）。两源读败/报价缺=fail-closed 拒产（溢价未知不臆放）。
// 只挡 DCI：earn/其他模板零动；D-509 bound 照旧复用同一选档界。
package oppmatch

import (
	"context"
	"fmt"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// dciPremiumFloor D-514 闸地板（业主令钉值：8%）。
const dciPremiumFloor = 0.08

// dciStrikeBound D-509 快照同界：现货参照×约束表 OTM 界（opt=PUT→put ratio，
// CALL→call ratio；无参照/界行缺→0=无界快照降级）。
func dciStrikeBound(ctx context.Context, st *pgstore.Store, opt string) float64 {
	spot, err := st.LatestSpreadSpotRef(ctx, "BTC")
	if err != nil || spot <= 0 {
		return 0
	}
	cons, err := st.ListCapabilityConstraints(ctx)
	if err != nil {
		return 0
	}
	cb, pb := gate.OTMBoundRatios(cons)
	ratio := cb
	if opt == "PUT" {
		ratio = pb
	}
	if ratio == nil {
		return 0
	}
	return gate.StrikeBound(spot, *ratio)
}

// dciPremiumGate 溢价闸判定：返回过闸与否+拒因注记（「溢价低于闸」语义）。
func dciPremiumGate(ctx context.Context, st *pgstore.Store, venue string) (bool, string) {
	qs, err := st.ListDCIQuotes(ctx, venue, "BTC", "PUT", "USDT", 90,
		dciStrikeBound(ctx, st, "PUT"))
	if err != nil {
		return false, fmt.Sprintf("%s PUT 报价读败：%v", venue, err)
	}
	if len(qs) == 0 || qs[0].APY <= 0 {
		return false, fmt.Sprintf("%s PUT 达标档报价缺（溢价未知不臆放）", venue)
	}
	// 单位口径按 venue 采集面归一小数：binance apy 列=%口径实拍（cexscan/dci.go
	// 注）→/100；okx annualizedYield 本身就是小数形（0.0541=5.41%）→原值。
	premium := qs[0].APY
	if venue == "binance" {
		premium /= 100
	}
	// 基准=同币种（投入币 USDT）跨 venue 理财顶档——异币种营销档不入基准
	// （BETH 365% 类 3× 地板虚高实证）；earn 面缺=退纯地板不臆放。
	benchVenue, earnApr, benchTS, ok, err := st.MaxEarnAPRByCcy(ctx, "USDT")
	if err != nil {
		return false, fmt.Sprintf("earn_apr USDT 基准读败：%v", err)
	}
	if !ok {
		earnApr = 0 // earn 面缺≠无限额——闸退纯地板 8%
	}
	thr := dciPremiumFloor
	if t := 3 * earnApr; t > thr {
		thr = t
	}
	if premium < thr {
		return false, fmt.Sprintf("溢价低于闸：premium_ann %.2f%% < max(3×USDT_earn %s %.2f%%@%s, 8%%)=%.2f%%",
			premium*100, benchVenue, earnApr*100, benchTS.Format("01-02 15:04"), thr*100)
	}
	return true, fmt.Sprintf("基准=USDT earn_apr %s %.2f%% @%s", benchVenue, earnApr*100, benchTS.Format("01-02 15:04"))
}
