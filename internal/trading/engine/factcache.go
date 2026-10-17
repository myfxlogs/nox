package engine

import (
	"sync"
	"sync/atomic"
	"time"

	"nox/internal/data/fact"
)

// FactCache 实现 detector.FactProvider，从内存缓存提供 funding/swap/trade_mode Fact。
// P1#8b：读侧零拷贝——写入路径持锁改可变态后重建只读快照 Store 到 atomic.Pointer，
// 读方法直接 Load() 返回共享只读 map（**返回值只读，调用方禁修改**；后续写入不影响
// 已取走的旧快照）。帧率级 ScanAll 每帧调四读法，原深拷贝占 CPU 33%（pprof 实证）。
type FactCache struct {
	mu      sync.Mutex                       // 仅写侧
	funding map[string]map[string]float64    // canonical → venue → 年化
	swap    map[string]map[string][2]float64 // canonical → venue → [long, short]
	// tradeMode 键口径与 swap 不同（D-060 F4）：canonical → venueKey（"{venue}:{src}"，
	// Fact.Venue+":"+Fact.Src 拼，MT 侧 src=mt5/mt4）——消费方是 VenueKey() 语法
	// （检测器腿/执行腿），swap 消费方是 Quote.Venue 名键，两者各按各自消费方，不得互相对齐。
	// P1#8b：TTL 读侧过滤退役——trade_mode 权威判定在 exec 即时探针（P1#2），发现层
	// 只做负向剔除，过滞条目无害（不再为 TTL 每帧遍历过滤）。
	tradeMode map[string]map[string]int64
	isReal    map[string]*bool // D-178：venue:type → *bool（nil=未知）

	// D-604 N3：funding/swap 惰性判龄——写侧存 at+minAt，读侧仅 minAt 超龄
	// 才逐键滤（全鲜时零扫描，P1#8b CPU 先例）；零值 maxAge=禁用保旧行为。
	fundingAt, swapAt map[string]map[string]time.Time
	minFAt, minSAt    time.Time
	fundingMaxAge     atomic.Int64 // time.Duration 纳秒，0=禁用
	swapMaxAge        atomic.Int64

	snapFunding atomic.Pointer[map[string]map[string]float64]
	snapSwap    atomic.Pointer[map[string]map[string][2]float64]
	snapTrade   atomic.Pointer[map[string]map[string]int64]
	snapReal    atomic.Pointer[map[string]*bool]
}

// SetAgeGates 惰性判龄窗（cmd/core 装配——窗值与 discover/scan 侧同源注入）。
func (fc *FactCache) SetAgeGates(fundingMaxAge, swapMaxAge time.Duration) {
	fc.fundingMaxAge.Store(int64(fundingMaxAge))
	fc.swapMaxAge.Store(int64(swapMaxAge))
}

func NewFactCache() *FactCache {
	fc := &FactCache{
		funding:   make(map[string]map[string]float64),
		swap:      make(map[string]map[string][2]float64),
		tradeMode: make(map[string]map[string]int64),
		isReal:    make(map[string]*bool),
		fundingAt: make(map[string]map[string]time.Time),
		swapAt:    make(map[string]map[string]time.Time),
	}
	fc.rebuildAll()
	return fc
}

func (fc *FactCache) UpdateFacts(facts []fact.Fact) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	var dirtyF, dirtyS, dirtyT bool
	for _, f := range facts {
		switch f.Kind {
		case fact.KindFunding:
			if fc.funding[f.Symbol] == nil {
				fc.funding[f.Symbol] = make(map[string]float64)
				fc.fundingAt[f.Symbol] = make(map[string]time.Time)
			}
			fc.funding[f.Symbol][f.Venue] = f.Value
			fc.fundingAt[f.Symbol][f.Venue] = f.Ts
			if fc.minFAt.IsZero() || f.Ts.Before(fc.minFAt) {
				fc.minFAt = f.Ts
			}
			dirtyF = true
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
			if fc.swapAt[f.Symbol] == nil {
				fc.swapAt[f.Symbol] = make(map[string]time.Time)
			}
			fc.swapAt[f.Symbol][f.Venue] = f.Ts
			if fc.minSAt.IsZero() || f.Ts.Before(fc.minSAt) {
				fc.minSAt = f.Ts
			}
			dirtyS = true
		case fact.KindTradeMode:
			if fc.tradeMode[f.Symbol] == nil {
				fc.tradeMode[f.Symbol] = make(map[string]int64)
			}
			fc.tradeMode[f.Symbol][f.Venue+":"+f.Src] = int64(f.Value)
			dirtyT = true
		}
	}
	// 写时重建只读快照（写频率=WS 批/h 级，远低于帧率读）。
	if dirtyF {
		fc.snapFunding.Store(ptrOf(clone2(fc.funding)))
	}
	if dirtyS {
		fc.snapSwap.Store(ptrOf(clone2(fc.swap)))
	}
	if dirtyT {
		fc.snapTrade.Store(ptrOf(clone2(fc.tradeMode)))
	}
}

// ptrOf 取局部变量指针（快照 Store 入参）。
func ptrOf[V any](v V) *V { return &v }

// clone2 二级 map 深拷贝（写侧专用，产物=只读快照）。
func clone2[V any](src map[string]map[string]V) map[string]map[string]V {
	cp := make(map[string]map[string]V, len(src))
	for k, v := range src {
		inner := make(map[string]V, len(v))
		for kk, vv := range v {
			inner[kk] = vv
		}
		cp[k] = inner
	}
	return cp
}

// clone1 一级 map 拷贝（is_real 快照用）。
func clone1[V any](src map[string]V) map[string]V {
	cp := make(map[string]V, len(src))
	for k, v := range src {
		cp[k] = v
	}
	return cp
}

// rebuildAll boot 初始化：四快照指向空只读 map（读侧永不遇 nil pointer）。
func (fc *FactCache) rebuildAll() {
	fc.snapFunding.Store(ptrOf(clone2(fc.funding)))
	fc.snapSwap.Store(ptrOf(clone2(fc.swap)))
	fc.snapTrade.Store(ptrOf(clone2(fc.tradeMode)))
	fc.snapReal.Store(ptrOf(clone1(fc.isReal)))
}

// FundingFacts 只读快照：canonical → venue → 年化（禁修改返回值）。
// D-604 N3 惰性判龄：minAt 未超龄=直返快照零扫描；超龄=逐键滤（写侧 at 伴随）。
func (fc *FactCache) FundingFacts() map[string]map[string]float64 {
	snap := *fc.snapFunding.Load()
	maxAge := time.Duration(fc.fundingMaxAge.Load())
	if maxAge <= 0 || fc.minFAt.IsZero() || time.Since(fc.minFAt) <= maxAge {
		return snap
	}
	out := map[string]map[string]float64{}
	for sym, inner := range snap {
		for v, val := range inner {
			if time.Since(fc.fundingAt[sym][v]) <= maxAge {
				if out[sym] == nil {
					out[sym] = map[string]float64{}
				}
				out[sym][v] = val
			}
		}
	}
	return out
}

// SwapFacts 只读快照：canonical → venue → [long, short]（禁修改返回值）。
// D-604 N3 惰性判龄同 FundingFacts（swap 窗独立）。
func (fc *FactCache) SwapFacts() map[string]map[string][2]float64 {
	snap := *fc.snapSwap.Load()
	maxAge := time.Duration(fc.swapMaxAge.Load())
	if maxAge <= 0 || fc.minSAt.IsZero() || time.Since(fc.minSAt) <= maxAge {
		return snap
	}
	out := map[string]map[string][2]float64{}
	for sym, inner := range snap {
		for v, val := range inner {
			if time.Since(fc.swapAt[sym][v]) <= maxAge {
				if out[sym] == nil {
					out[sym] = map[string][2]float64{}
				}
				out[sym][v] = val
			}
		}
	}
	return out
}

// TradeModes 只读快照：canonical → venueKey → mode（禁修改返回值）。
// 原 TTL 读侧过滤已在 P1#8b 退役（见结构体注释）。
func (fc *FactCache) TradeModes() map[string]map[string]int64 {
	return *fc.snapTrade.Load()
}

// UpdateIsRealMap D-178：覆盖写 is_real 映射（boot 加载 + fact sink 周期刷新，幂等无害）。
func (fc *FactCache) UpdateIsRealMap(m map[string]*bool) {
	fc.mu.Lock()
	fc.isReal = m
	fc.snapReal.Store(ptrOf(clone1(m)))
	fc.mu.Unlock()
}

// IsRealMap D-178：只读快照 venue:type → *bool（nil=未知；禁修改返回值）。
func (fc *FactCache) IsRealMap() map[string]*bool {
	return *fc.snapReal.Load()
}
