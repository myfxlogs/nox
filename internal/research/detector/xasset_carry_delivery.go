// xasset_carry_delivery.go —— D-649续③ perp_delivery 机会喂入环：armed 交割
// 配对提示型+扫描件（自 xasset_carry.go 折分，行数门禁）。
// 断供病灶：armed 组合（xasset_allow_combos enabled）≠发现层扫——毕业批
// 「放行资格」与「机会候选产出」两段；asset_equiv/cfd-hint 面均无 futures
// 腿 canonical 配对（ETH-USD_UM-261225 类）→opportunities 表零 ETH okx×okx
// 行，jit/sizing/执行全链到不了（combo#25 实证）。
package detector

import (
	"context"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
)

// DeliveryPairHint armed 交割配对行（spot×futures 或 futures×spot——venueKey/
// canonical 已解析，TenorDays=交割剩余天数供 basis 年化）。
type DeliveryPairHint struct {
	BuyCanonical  string
	SellCanonical string
	BuyVenueKey   string
	SellVenueKey  string
	BuyTenorDays  float64 // buy 腿 vt=futures 时填
	SellTenorDays float64 // sell 腿 vt=futures 时填
}

// DeliveryHintSource 交割配对提示读取口（照 AssetEquivSource/CfdPairHintSource
// 模式：单实例注入禁 Scan 内查库；nil=跳过）。
type DeliveryHintSource interface {
	DeliveryPairs(ctx context.Context) []DeliveryPairHint
}

// scanDeliveryHints armed 交割配对扫描：vt 入 AssetEquivEntry（perp 分支不吃
// funding=0 正构型），TenorDays 喂 basis 年化；复用 tryPair 全门禁——
// 跟踪误差在 futures 腿内豁免（价差即机会）。
func (d *XAssetCarryDetector) scanDeliveryHints(
	ctx context.Context, snapshot map[string]map[string]bus.Quote,
	funding map[string]map[string]float64, swap map[string]map[string][2]float64,
	modes map[string]map[string]int64, realMap map[string]*bool, now time.Time,
) []Candidate {
	if d.DeliveryHints == nil {
		return nil
	}
	var out []Candidate
	for _, h := range d.DeliveryHints.DeliveryPairs(ctx) {
		bq, ok := snapshot[h.BuyCanonical][h.BuyVenueKey]
		if !ok {
			continue
		}
		sq, ok := snapshot[h.SellCanonical][h.SellVenueKey]
		if !ok {
			continue
		}
		be := AssetEquivEntry{Venue: bareVenue(h.BuyVenueKey), VenueType: keydomain.TypeOf(h.BuyVenueKey), TenorDays: h.BuyTenorDays}
		se := AssetEquivEntry{Venue: bareVenue(h.SellVenueKey), VenueType: keydomain.TypeOf(h.SellVenueKey), TenorDays: h.SellTenorDays}
		if c := d.tryPair("", h.BuyCanonical, be, bq, h.SellCanonical, se, sq, funding, swap, modes, realMap, now); c != nil {
			out = append(out, *c)
		}
	}
	return out
}
