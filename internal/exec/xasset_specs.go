// xasset_specs.go D-403 S3：白名单表行→spec 共享映射（启动装配 xassetAllowFromTable 与
// engine 热重载 ReloadXAssetAllowCombos 同源禁复制；exec→pgstore 既有依赖方向合法）。
package exec

import "arb/internal/store/pgstore"

// XAssetSpecsFromRows pgstore 白名单行映射为 exec spec（结构同构零信息损失约束同
// pgstore.XAssetAllowRow）。D-367 S3 加载期校验（fail-closed 零信任）规则单点在此：
// notional 显式非正=坏配置行不授权任何交易，整行剔除+计数返回。
// D-010：margin_usd 同规（显式非正剔除）；notional+margin 并存=双口径歧义行剔除
// （DB CHECK 已钉死，此处为防御纵深——脏数据行不授权任何交易）。
func XAssetSpecsFromRows(rows []pgstore.XAssetAllowRow) (specs []XAssetComboAllowSpec, dropped int) {
	specs = make([]XAssetComboAllowSpec, 0, len(rows))
	for _, r := range rows {
		if r.NotionalUSD != nil && *r.NotionalUSD <= 0 {
			dropped++
			continue
		}
		if r.MarginUSD != nil && *r.MarginUSD <= 0 {
			dropped++
			continue
		}
		if r.NotionalUSD != nil && r.MarginUSD != nil {
			dropped++
			continue
		}
		specs = append(specs, XAssetComboAllowSpec{
			CanonicalBuy: r.CanonicalBuy, CanonicalSell: r.CanonicalSell,
			VenueBuy: r.VenueBuy, VenueSell: r.VenueSell,
			MaxLongQty: r.MaxLongQty, MaxShortQty: r.MaxShortQty, MaxGrossUSD: r.MaxGrossUSD,
			NotionalUSD: r.NotionalUSD, MarginUSD: r.MarginUSD,
		})
	}
	return specs, dropped
}
