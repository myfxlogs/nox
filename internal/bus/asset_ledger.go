// asset_ledger.go —— D-192：CEX 现货资产级余额活缓存（内存态主源）。
// 私有流 outboundAccountPosition 全资产 B[{a,f,l}] 落面（非只 USDT 池聚合）；
// 存活闸读侧=dephealth 流连接态（连接级而非事件新旧——票面钦定），
// 死流消费方走 REST 兜底。边界=现货域；earn 子账不入此面（R6 REST 径）。
package bus

import (
	"strconv"
	"sync"
	"time"
)

// AssetBal 单资产余额条目。
type AssetBal struct {
	Free   float64
	Locked float64
	At     time.Time
}

// AssetLedger venueKey→asset→余额活缓存。
type AssetLedger struct {
	mu sync.RWMutex
	m  map[string]map[string]AssetBal
}

// NewAssetLedger 创建资产级活缓存。
func NewAssetLedger() *AssetLedger { return &AssetLedger{m: map[string]map[string]AssetBal{}} }

// SpotAssets binance:spot 全局资产级活缓存（cex-io 写、dciexec 读——
// 装配穿层免线，包级单件同 Topic 先例）。
var SpotAssets = NewAssetLedger()

// Put 单资产写/覆盖（流事件同源入口）。
func (l *AssetLedger) Put(venueKey, asset string, free, locked float64, at time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.m[venueKey] == nil {
		l.m[venueKey] = map[string]AssetBal{}
	}
	l.m[venueKey][asset] = AssetBal{Free: free, Locked: locked, At: at}
	l.mu.Unlock()
}

// PutAll outboundAccountPosition 全帧批量落（f/l 字符串解析败跳过该资产不连坐）。
func (l *AssetLedger) PutAll(venueKey string, rows []struct{ A, F, L string }, at time.Time) {
	if l == nil {
		return
	}
	for _, r := range rows {
		f, ferr := strconv.ParseFloat(r.F, 64)
		lk, lerr := strconv.ParseFloat(r.L, 64)
		if ferr != nil || lerr != nil {
			continue
		}
		l.Put(venueKey, r.A, f, lk, at)
	}
}

// Get 读单资产（ok=false=该资产无流值——读侧走 REST 兜底）。
func (l *AssetLedger) Get(venueKey, asset string) (AssetBal, bool) {
	if l == nil {
		return AssetBal{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	b, ok := l.m[venueKey][asset]
	return b, ok
}
