// combo_underlying.go —— 经济经验库回改点落地（消化链建制票③机械直译闸）：
// proxy-pair-not-convergence 条目回改点 combo_gate:underlying-consistency——
// combo 落册前底层一致性校验（universe hedge_canonical + asset_equiv 类 + 价位
// 量级三源互证，#20 同型病灶机械防复：不同产品身份按「同资产代理」配对落册）。
//
// 语义：任何一源给出「确定性冲突」→ 拒写；数据缺席=弃权放行（与坑签名闸同
// fail-open 面——无据不为难合法组合；冲突证据=已证错配非疑错配）。读径败
// （universe/equiv 查询错）=拒写（钱径写闸 fail-closed，脏读判放行=病灶）。
package pgstore

import (
	"context"
	"fmt"
	"math"

	"nox/internal/data/keydomain"
)

// comboMagRatio 价位量级背离阈：两腿中间价比 >4× 或 <0.25×=量纲级错配
// （同标的对冲两腿价应同量级；缺席价=弃权不判）。
const comboMagRatio = 4.0

// InsertXAssetAllowCombo 公开写径（棘轮拆出——闸调用面）：
// proxy-pair 回改点机械直译闸（#20 同型防复）→ insertXAssetAllowCombo 落库。
// approve/S2桥/admin 全写径同闸覆盖（原函数名保留=零调用点改动）。
func (s *Store) InsertXAssetAllowCombo(ctx context.Context, r *XAssetComboInsert) (int64, error) {
	if err := s.checkComboUnderlying(ctx, r); err != nil {
		return 0, err
	}
	return s.insertXAssetAllowCombo(ctx, r)
}

// checkComboUnderlying 三源互证主件（InsertXAssetAllowCombo 写径调用）。
func (s *Store) checkComboUnderlying(ctx context.Context, r *XAssetComboInsert) error {
	uni, err := s.ListCexUniverse(ctx)
	if err != nil {
		return fmt.Errorf("combo 底层校验 universe 读败: %w", err)
	}
	equiv, err := s.ListAssetEquiv(ctx)
	if err != nil {
		return fmt.Errorf("combo 底层校验 asset_equiv 读败: %w", err)
	}
	// canonical|venue → asset_class（venue 维先中，裸 canonical 回落）。
	classOf := func(canon, venue string) (string, bool) {
		fallback := ""
		for _, e := range equiv {
			if e.Canonical != canon {
				continue
			}
			if e.Venue == venue {
				return e.AssetClass, true
			}
			if fallback == "" {
				fallback = e.AssetClass
			}
		}
		return fallback, fallback != ""
	}
	type legEv struct {
		canon, venue string
		hedge        string // universe.hedge_canonical（CEX 腿源）
		class        string // asset_equiv 类
		mid          float64
		hasMid       bool
	}
	var legs [2]legEv
	for i, l := range [2]struct{ canon, venueKey string }{
		{r.CanonicalBuy, r.VenueBuy}, {r.CanonicalSell, r.VenueSell},
	} {
		v := keydomain.Parse(l.venueKey)
		legs[i].canon, legs[i].venue = l.canon, v.Venue
		// 源① universe：CEX 腿 hedge_canonical（该 symbol 的对冲身份锚）。
		if !keydomain.IsMTKey(l.venueKey) {
			if row, ok := uni[v.Venue+"|"+l.canon]; ok {
				legs[i].hedge = row.HedgeCanonical
			}
		}
		// 源② asset_equiv 类成员。
		if cls, ok := classOf(l.canon, v.Venue); ok {
			legs[i].class = cls
		}
		// 源③ 价位量级（ticks mid 最新值；MT 腿 canonical≈symbol 已归一）。
		if mid, ok := s.LatestTickMid(ctx, v.Venue, l.canon); ok {
			legs[i].mid, legs[i].hasMid = mid, true
		}
	}
	a, b := legs[0], legs[1]
	// 判① hedge_canonical 身份锚：CEX 腿 hedge 非空则必须与对方 canonical
	// 同身份（=或同类）。#20 病灶=SPCXUSD1 hedge=SPX500 × 幻影 US500 落册。
	for i, l := range legs {
		if l.hedge == "" {
			continue
		}
		other := legs[1-i]
		if l.hedge == other.canon {
			continue
		}
		if cls, ok := classOf(other.canon, other.venue); ok && cls == l.class {
			continue // hedge≠canon 但同类（等价物对冲合法）
		}
		return fmt.Errorf("combo 底层一致性拒：%s hedge_canonical=%s × 对方 canonical=%s（同身份/同类均不符——proxy-pair 回改闸）",
			l.canon, l.hedge, other.canon)
	}
	// 判② 双腿类成员互斥：两腿都解析出类且不同 → 拒。
	if a.class != "" && b.class != "" && a.class != b.class {
		return fmt.Errorf("combo 底层一致性拒：%s(%s)×%s(%s) 类成员互斥——proxy-pair 回改闸",
			a.canon, a.class, b.canon, b.class)
	}
	// 判③ 价位量级：双腿价俱在且比超阈 → 拒（量纲级错配）。
	if a.hasMid && b.hasMid && a.mid > 0 && b.mid > 0 {
		ratio := math.Max(a.mid, b.mid) / math.Min(a.mid, b.mid)
		if ratio > comboMagRatio {
			return fmt.Errorf("combo 底层一致性拒：价位量级背离 %s=%.4g × %s=%.4g（ratio %.1f > %.0f——proxy-pair 回改闸）",
				a.canon, a.mid, b.canon, b.mid, ratio, comboMagRatio)
		}
	}
	return nil
}
