// xasset_specs.go D-403 S3：白名单表行→spec 共享映射（启动装配 xassetAllowFromTable 与
// engine 热重载 ReloadXAssetAllowCombos 同源禁复制；exec→pgstore 既有依赖方向合法）。
package exec

import "arb/internal/store/pgstore"

// XAssetSpecsFromRows pgstore 白名单行映射为 exec spec（结构同构零信息损失约束同
// pgstore.XAssetAllowRow）。D-367 S3 加载期校验（fail-closed 零信任）规则单点在此：
// notional 显式非正=坏配置行不授权任何交易，整行剔除+计数返回。
func XAssetSpecsFromRows(rows []pgstore.XAssetAllowRow) (specs []XAssetComboAllowSpec, dropped int) {
	specs = make([]XAssetComboAllowSpec, 0, len(rows))
	for _, r := range rows {
		if r.NotionalUSD != nil && *r.NotionalUSD <= 0 {
			dropped++
			continue
		}
		specs = append(specs, XAssetComboAllowSpec{
			CanonicalBuy: r.CanonicalBuy, CanonicalSell: r.CanonicalSell,
			VenueBuy: r.VenueBuy, VenueSell: r.VenueSell,
			MaxLongQty: r.MaxLongQty, MaxShortQty: r.MaxShortQty, MaxGrossUSD: r.MaxGrossUSD,
			NotionalUSD: r.NotionalUSD,
		})
	}
	return specs, dropped
}
