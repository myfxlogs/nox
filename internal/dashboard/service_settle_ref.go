// D-318 结算口径参照：机会页「净收益/年」主值为当期预测费率（premium index 驱动盘中漂移），
// 本文件派生「近 7d funding_history 实际结算均值代入同公式」的参照净息差（settleNet）。
// 公式（符号口径与 D-312 recalcNetFunding 一致）：settleNet = netBps − Σ sign×predRate + Σ sign×settleAvg；
// 买腿 sign=−1、卖腿 sign=+1（SELL 收 funding、BUY 付）；缺 pred/缺 settleAvg/无 perp 腿
// → 三 proto 字段缺省（fail-缺省非 fail-0）。缓存模式参照 aliasMu/aliasMap（RWMutex+TTL 双检）。
package dashboard

import (
	"context"
	"strings"
	"sync"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

const (
	settleTTL      = 10 * time.Minute   // funding_history 日批量变更慢
	predTTL        = 60 * time.Second   // 当期预测 5min 采集轮，60s 缓存与漂移同量级
	settleLookback = 7 * 24 * time.Hour // 近 7d 窗
)

// settleRefCache 参照值双缓存（线程安全；Service 持值，nil map=未加载）。
type settleRefCache struct {
	mu       sync.RWMutex
	settle   map[string]map[string]pgstore.SettleAvg // venue → symbol → 7d 结算均值
	settleAt time.Time
	pred     map[string]map[string]float64 // bare venue → symbol → 当期预测（年化百分点）
	predAt   time.Time
}

// settleAvgs 取 7d 结算均值（缓存命中直接返回，过期/未加载拉取回填；双检防击穿）。
func (s *Service) settleAvgs(ctx context.Context) (map[string]map[string]pgstore.SettleAvg, error) {
	s.settleRef.mu.RLock()
	if s.settleRef.settle != nil && time.Since(s.settleRef.settleAt) < settleTTL {
		out := s.settleRef.settle
		s.settleRef.mu.RUnlock()
		return out, nil
	}
	s.settleRef.mu.RUnlock()

	s.settleRef.mu.Lock()
	defer s.settleRef.mu.Unlock()
	if s.settleRef.settle != nil && time.Since(s.settleRef.settleAt) < settleTTL {
		return s.settleRef.settle, nil
	}
	avgs, err := s.store.FundingSettleAvgs(ctx, time.Now().Add(-settleLookback))
	if err != nil {
		return nil, err
	}
	s.settleRef.settle, s.settleRef.settleAt = avgs, time.Now()
	return avgs, nil
}

// predRates 取当期预测费率：LatestFacts(kind=funding, venue="", symbol="") 一次拉全，
// 建 map[bare venue][symbol]value（与扫描/评估器同一数据源）。
func (s *Service) predRates(ctx context.Context) (map[string]map[string]float64, error) {
	s.settleRef.mu.RLock()
	if s.settleRef.pred != nil && time.Since(s.settleRef.predAt) < predTTL {
		out := s.settleRef.pred
		s.settleRef.mu.RUnlock()
		return out, nil
	}
	s.settleRef.mu.RUnlock()

	s.settleRef.mu.Lock()
	defer s.settleRef.mu.Unlock()
	if s.settleRef.pred != nil && time.Since(s.settleRef.predAt) < predTTL {
		return s.settleRef.pred, nil
	}
	facts, err := s.store.LatestFacts(ctx, "funding", "", "")
	if err != nil {
		return nil, err
	}
	m := make(map[string]map[string]float64)
	for _, f := range facts {
		if m[f.Venue] == nil {
			m[f.Venue] = make(map[string]float64)
		}
		m[f.Venue][f.Symbol] = f.Value
	}
	s.settleRef.pred, s.settleRef.predAt = m, time.Now()
	return m, nil
}

// fillSettleRef 机会卡 settle 参照字段填充（D-318 S2）：
// settleNet = NetBps − Σ sign×predRate + Σ sign×settleAvg（仅 :perp 腿参与，非 perp 腿原样保留残差）；
// settle_net_usd 复用 netUSD 同 notional 派生；settle_samples=各 perp 腿期数取小。
// 任一 perp 腿缺 pred/缺 settleAvg、无 perp 腿、非息差型、store 查询失败 → 全部缺省不填。
func (s *Service) fillSettleRef(ctx context.Context, pb *v1.Opportunity, o *pgstore.Opportunity) {
	if !isCarryType(o.Type) {
		return
	}
	if s.store == nil {
		return // 未接线 store（单测/裸 Service）→ 缺省
	}
	pred, err := s.predRates(ctx)
	if err != nil {
		return
	}
	settle, err := s.settleAvgs(ctx)
	if err != nil {
		return
	}
	buySymbol, sellSymbol := o.Canonical, o.Canonical
	if o.CanonicalSell != nil && *o.CanonicalSell != "" {
		sellSymbol = *o.CanonicalSell
	}
	settleNet := o.NetBps
	samples, perpLegs := 0, 0
	for _, lg := range []struct {
		key    string
		sign   float64
		symbol string
	}{
		{o.VenueBuy, -1, buySymbol},
		{o.VenueSell, 1, sellSymbol},
	} {
		if !strings.HasSuffix(lg.key, ":perp") {
			continue
		}
		bare := strings.TrimSuffix(lg.key, ":perp")
		pr, ok := pred[bare][lg.symbol]
		if !ok {
			return
		}
		sa, ok := settle[bare][lg.symbol]
		if !ok {
			return
		}
		settleNet += lg.sign * (sa.Avg - pr)
		if perpLegs == 0 || sa.N < samples {
			samples = sa.N
		}
		perpLegs++
	}
	if perpLegs == 0 {
		return
	}
	pb.SettleNetBps = &settleNet
	usd := netUSD(settleNet, o.Type, s.notionalUSD)
	pb.SettleNetUsd = &usd
	samples32 := int32(samples)
	pb.SettleSamples = &samples32
}
