package engine

import (
	"sync"
	"time"

	"arb/internal/fact"
)

// FactCache 实现 detector.FactProvider，从内存缓存提供 funding/swap/trade_mode Fact。
type FactCache struct {
	mu      sync.RWMutex
	funding map[string]map[string]float64    // canonical → venue → 年化
	swap    map[string]map[string][2]float64 // canonical → venue → [long, short]
	// tradeMode 键口径与 swap 不同（D-060 F4）：canonical → venueKey（"{venue}:{src}"，
	// Fact.Venue+":"+Fact.Src 拼，MT 侧 src=mt5/mt4）——消费方是 VenueKey() 语法
	// （检测器腿/执行腿），swap 消费方是 Quote.Venue 名键，两者各按各自消费方，不得互相对齐。
	tradeMode    map[string]map[string]tradeModeEntry
	tradeModeTTL time.Duration
}

// tradeModeEntry trade_mode 值 + 到达时间（TTL 新鲜度判据）。
type tradeModeEntry struct {
	mode int64
	ts   time.Time
}

func NewFactCache() *FactCache {
	return &FactCache{
		funding:      make(map[string]map[string]float64),
		swap:         make(map[string]map[string][2]float64),
		tradeMode:    make(map[string]map[string]tradeModeEntry),
		tradeModeTTL: 24 * time.Hour, // 缺省兜底；main 注入 config.TradeModeTTL（D-060）
	}
}

// SetTradeModeTTL 注入 trade_mode 新鲜度（config.ARB_TRADEMODE_TTL）。boot 期一次性调用。
func (fc *FactCache) SetTradeModeTTL(d time.Duration) {
	if d <= 0 {
		return
	}
	fc.mu.Lock()
	fc.tradeModeTTL = d
	fc.mu.Unlock()
}

func (fc *FactCache) UpdateFacts(facts []fact.Fact) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	for _, f := range facts {
		switch f.Kind {
		case fact.KindFunding:
			if fc.funding[f.Symbol] == nil {
				fc.funding[f.Symbol] = make(map[string]float64)
			}
			fc.funding[f.Symbol][f.Venue] = f.Value
		case fact.KindSwapLong, fact.KindSwapShort:
			if fc.swap[f.Symbol] == nil {
				fc.swap[f.Symbol] = make(map[string][2]float64)
			}
			entry := fc.swap[f.Symbol][f.Venue]
			if f.Kind == fact.KindSwapLong {
				entry[0] = f.Value
			} else {
				entry[1] = f.Value
			}
			fc.swap[f.Symbol][f.Venue] = entry
		case fact.KindTradeMode:
			if fc.tradeMode[f.Symbol] == nil {
				fc.tradeMode[f.Symbol] = make(map[string]tradeModeEntry)
			}
			fc.tradeMode[f.Symbol][f.Venue+":"+f.Src] = tradeModeEntry{mode: int64(f.Value), ts: f.Ts}
		}
	}
}

func (fc *FactCache) FundingFacts() map[string]map[string]float64 {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	cp := make(map[string]map[string]float64, len(fc.funding))
	for k, v := range fc.funding {
		cp[k] = make(map[string]float64, len(v))
		for kk, vv := range v {
			cp[k][kk] = vv
		}
	}
	return cp
}

func (fc *FactCache) SwapFacts() map[string]map[string][2]float64 {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	cp := make(map[string]map[string][2]float64, len(fc.swap))
	for k, v := range fc.swap {
		cp[k] = make(map[string][2]float64, len(v))
		for kk, vv := range v {
			cp[k][kk] = vv
		}
	}
	return cp
}

// TradeModes 返回 TTL 内新鲜的 trade_mode（canonical → venueKey → mode，D-060 F4）。
// 过期条目不出现 = 未知 → 消费方放行（检测器过滤/执行预检同口径）。
func (fc *FactCache) TradeModes() map[string]map[string]int64 {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	now := time.Now()
	cp := make(map[string]map[string]int64, len(fc.tradeMode))
	for canon, vm := range fc.tradeMode {
		for vk, e := range vm {
			if now.Sub(e.ts) > fc.tradeModeTTL {
				continue
			}
			if cp[canon] == nil {
				cp[canon] = make(map[string]int64, len(vm))
			}
			cp[canon][vk] = e.mode
		}
	}
	return cp
}

// TradeMode 查单腿 trade_mode（F5 执行预检用；TTL 过期/未知 → ok=false 放行）。
func (fc *FactCache) TradeMode(canonical, venueKey string) (int64, bool) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	e, ok := fc.tradeMode[canonical][venueKey]
	if !ok || time.Since(e.ts) > fc.tradeModeTTL {
		return 0, false
	}
	return e.mode, true
}
