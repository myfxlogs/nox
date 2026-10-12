package main

import (
	"context"
	"log"

	"nox/internal/data/collect"
	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
	"nox/internal/trading/engine"
)

// makeFactSink 写 facts 到 DB + 更新 FactCache（§5b）。
func makeFactSink(st *pgstore.Store, fc *engine.FactCache) collect.Sink {
	return func(ctx context.Context, facts []fact.Fact) error {
		fc.UpdateFacts(facts)
		return st.InsertFacts(ctx, facts)
	}
}

// makeMetaAwareSink D-140 S2：在 makeFactSink 基础上拦截 mt_symbol_meta facts，
// 额外执行 CS 反哺（UpsertSymbolMappingCSOnly 仅填 NULL 行，人工行优先不覆盖）。
// D-173：拦截 mt_symbol_product facts，CalcMode → product_group 反哺
// （UpdateProductGroupByCanonical 按 (venue, canonical) 批量更新全部映射行）。
func makeMetaAwareSink(st *pgstore.Store, fc *engine.FactCache) collect.Sink {
	inner := makeFactSink(st, fc)
	return func(ctx context.Context, facts []fact.Fact) error {
		if err := inner(ctx, facts); err != nil {
			return err
		}
		// D-178：fact sink 周期顺便刷新 is_real 映射（幂等覆盖写，每 5min 一次 DB 查询）。
		if isRealMap, err := st.ListVenueIsReal(ctx); err == nil {
			fc.UpdateIsRealMap(isRealMap)
		}
		for _, f := range facts {
			// D-140 S2：CS 反哺（仅填 NULL 行，人工行优先不覆盖）
			if f.Kind == fact.KindMTSymbolMeta {
				filled, inserted, err := st.UpsertSymbolMappingCSOnly(ctx, f.Venue, f.Symbol, f.Value)
				if err != nil {
					log.Printf("[meta-sink] %s/%s CS upsert: %v", f.Venue, f.Symbol, err)
				}
				if filled > 0 {
					log.Printf("[meta-sink] %s/%s: CS filled (NULL→%v)", f.Venue, f.Symbol, f.Value)
				}
				if inserted > 0 {
					log.Printf("[meta-sink] %s/%s: new meta row (CS=%v, canonical pending)", f.Venue, f.Symbol, f.Value)
				}
			}
			// D-173：mt_symbol_product → product_group 反哺（CalcMode → product_group）。
			// 同 (venue, canonical) 多行（多 broker_symbol）批量统一；逐 fact 更新，
			// 查询侧 GROUP BY 众数聚合保证最终一致。
			if f.Kind != fact.KindMTSymbolProduct {
				continue
			}
			pg := fact.CalcModeToProductGroup(int32(f.Value))
			if pg == "" {
				continue
			}
			n, err := st.UpdateProductGroupByCanonical(ctx, f.Venue, f.Symbol, pg)
			if err != nil {
				log.Printf("[meta-sink] %s/%s product_group upsert: %v", f.Venue, f.Symbol, err)
				continue
			}
			if n > 0 {
				log.Printf("[meta-sink] %s/%s: product_group=%s (%d rows)", f.Venue, f.Symbol, pg, n)
			}
		}
		return nil
	}
}
