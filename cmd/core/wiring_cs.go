package main

// 合约规格解析器组（D-311 R2 从 wiring.go 拆出控行数）：
// MT 腿 broker_symbol_map / CEX perp 腿 fin_cex_universe.ct_val / pipeline 复合分派 /
// exec ContractSizeResolver 适配器 / 机会卡 USD 派生查询。

import (
	"context"
	"log"

	"nox/internal/data/keydomain"
	"time"

	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// legContractSizeResolver MT 腿合约规格查询（D-060 实施修正② 卖腿等名义反算）：
// venueKey 剥 venue 段 → broker_symbol_map.contract_size（D-042 backfilled）。执行低频路径，逐次查询即可。
func legContractSizeResolver(st *pgstore.Store) func(venueKey, venueSymbol string) (float64, bool) {
	return func(venueKey, venueSymbol string) (float64, bool) {
		venue := keydomain.BareOf(venueKey)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		maps, err := st.ListSymbolMappings(ctx, venue)
		if err != nil {
			log.Printf("[exec] leg contract size: list mappings %s: %v", venue, err)
			return 0, false
		}
		for _, m := range maps {
			if m.BrokerSymbol == venueSymbol && m.ContractSize != nil && *m.ContractSize > 0 {
				return *m.ContractSize, true
			}
		}
		return 0, false
	}
}

// legUnitQtyResolver MT 腿 canonical 单位数/手查询（D-594 S3）：venueKey 剥 venue 段 →
// broker_symbol_map contract_size × qty_scale。单位敏感 canonical 未标定行 → ok=false
// fail-closed；非敏感 NULL → cs×1.0。仅配 canonical 报价用（原生 PriceActual 链勿用）。
// w 非 nil 时超时计数（D-636 喂流看门狗 RPC 面哨兵征——超时簇=接入层劣化信号）。
func legUnitQtyResolver(st *pgstore.Store, w *mtRPCFaceWatch) func(venueKey, venueSymbol string) (float64, bool) {
	return func(venueKey, venueSymbol string) (float64, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		u, ok, err := st.UnitQtyByBrokerSymbol(ctx, venueKey, venueSymbol)
		if err != nil {
			if w != nil {
				w.timeouts.Add(1)
			}
			log.Printf("[exec] leg unit qty %s %s: %v", venueKey, venueSymbol, err)
			return 0, false
		}
		return u, ok
	}
}

// primarySymbolResolver D-383 A2.4：执行解析 primary-only（venueKey 剥 venue 段 →
// ResolvePrimarySymbol：primary/隐式 primary 行；多行组零 primary → 歧义态 fail-closed）。
// 执行低频路径，逐次查询即可。
func primarySymbolResolver(st *pgstore.Store) func(venueKey, canonical string) (string, error) {
	return func(venueKey, canonical string) (string, error) {
		venue := keydomain.BareOf(venueKey)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return st.ResolvePrimarySymbol(ctx, venue, canonical)
	}
}

// cexContractSizeResolver CEX perp 腿合约面值查询（D-311 S3）：bare venue+venueSymbol →
// fin_cex_universe.ct_val（雷达每轮 upsert：gate=quanto_multiplier/okx=ctVal/binance=1.0）。
// 执行低频路径，逐次查询即可。缺行/NULL/≤0 → ok=false（legContractSizeOf 上游 fail-closed）。
func cexContractSizeResolver(st *pgstore.Store) func(venue, venueSymbol string) (float64, bool) {
	return func(venue, venueSymbol string) (float64, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cs, ok, err := st.GetCexContractSize(ctx, venue, venueSymbol)
		if err != nil {
			log.Printf("[exec] cex contract size %s %s: %v", venue, venueSymbol, err)
			return 0, false
		}
		return cs, ok
	}
}

// pipelineCSResolver 复合合约规格解析（D-311 R2）：按 venueKey 后缀分派——
// :perp→fin_cex_universe.ct_val（cexContractSizeResolver，缺行/NULL/≤0→ok=false）；
// :spot→1.0（CEX 现货 qty 即基础币数）；:mt4/:mt5 及其余→broker_symbol_map
// （legContractSizeResolver 现状，CEX 无行必 miss→ok=false fail-closed）。
// 消费方=组合门禁 gross 校验 + calcLegPnL（CEX perp 腿顺带 miss→真 ct_val）。
func pipelineCSResolver(st *pgstore.Store) func(venueKey, venueSymbol string) (float64, bool) {
	leg := legContractSizeResolver(st)
	cex := cexContractSizeResolver(st)
	return func(venueKey, venueSymbol string) (float64, bool) {
		switch {
		case keydomain.IsType(venueKey, "perp"):
			return cex(keydomain.BareOf(venueKey), venueSymbol)
		case keydomain.IsType(venueKey, "spot"):
			return 1.0, true
		default:
			return leg(venueKey, venueSymbol)
		}
	}
}

// execCSResolver 复用 legContractSizeResolver（入场 sizing 与出场 PnL 同源 CS，P3 单一真相）。
// D-311 R2：底层函数已由接线方换成 pipelineCSResolver 复合分派（本适配器签名不变）。
type execCSResolver func(venueKey, venueSymbol string) (float64, bool)

func (r execCSResolver) ContractSize(venueKey, venueSymbol string) (float64, bool) {
	return r(venueKey, venueSymbol)
}

var _ exec.ContractSizeResolver = execCSResolver(nil)

// oppContractSizeResolver 机会卡 USD 派生用 CS 查询（D-062）：venueKey 剥 venue 段 →
// broker_symbol_map 按 (venue, canonical) 取 contract_size（D-061 对称桶保证跨 venue 一致，
// venue_buy 侧即代表）。CEX 腿 CS=1（qty 即基础币数，与 legContractSizeOf 同语义）。
// 缺失/未映射 → ok=false → USD 字段全 0，前端回退 bps。
func oppContractSizeResolver(st *pgstore.Store) func(canonical, venueKey string) (float64, bool) {
	return func(canonical, venueKey string) (float64, bool) {
		if !keydomain.IsMTKey(venueKey) {
			return 1.0, true // CEX 腿：CS=1
		}
		venue := keydomain.BareOf(venueKey)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// D-594：机会卡价格（o.PriceBuy）是 canonical 口径 → 用 canonical 单位数/手
		// （UnitQtyByCanonical = contract_size × qty_scale，敏感未标定 fail-closed）。
		u, ok, err := st.UnitQtyByCanonical(ctx, venue, canonical)
		if err != nil {
			log.Printf("[dashboard] opp unit qty %s %s: %v", venue, canonical, err)
			return 0, false
		}
		return u, ok
	}
}
