// wire_watchlist.go —— D-369 S2（R1 修订）：radar_watchlist core 读侧——仅 provider
// （radar_watchlist 符号集供给，60s 轮询→atomic snapshot，经 Config.SymProvider 并入
// env 符号集）。R1 裁撤：combo 自动 seed 链路（seedSimCombosOnce/startComboAutoSeed）
// 全域下线——production 复审裁定 demo 轨自动放行越权（P1 三重死链），试单通道归 D-368
// 后续批/人工行。
package main

import (
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// watchlistSymbolProvider radar_watchlist ∪ graduation 腿符号集（原子快照，60s 刷新）。
// D-542 S3①：graduation 腿（pending/approved carry_graduations + 白名单 combos 的 CEX
// canonical）并入有效符号集——SKLUSDT 类毕业腿在架却缺订=入库层永不入场实证。
type watchlistSymbolProvider struct {
	st   *pgstore.Store
	mu   sync.RWMutex
	rows []pgstore.RadarWatchlistRow
	grad map[string]bool // graduation 腿 CEX 基币（全所适用）
}

// Symbols 实现 exchange.SymbolProvider——D-532 S1 venue 作用域：
// venues 含该 venue ∪ venues 空（全所适用，保现状语义）；空集=无动态符号。
// D-542：graduation 腿基币对全部 CEX venue 透出（ venue 专属归属不清时宁可全所订，
// 缺腿 tick 比多订更贵；MT venue 段不经此 provider）。
func (w *watchlistSymbolProvider) Symbols(venue string) []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]string, 0, len(w.rows)+len(w.grad))
	seen := map[string]bool{}
	for _, r := range w.rows {
		if len(r.Venues) == 0 || slices.Contains(r.Venues, venue) {
			if !seen[r.Symbol] {
				seen[r.Symbol] = true
				out = append(out, r.Symbol)
			}
		}
	}
	for base := range w.grad {
		if !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
	}
	return out
}

// refresh 拉一次 watchlist+graduation 腿符号（失败保留旧快照——报价不闪断）。
func (w *watchlistSymbolProvider) refresh(ctx context.Context) {
	rows, err := w.st.LoadRadarWatchlist(ctx)
	if err != nil {
		log.Printf("[watchlist] load: %v (保留旧快照)", err)
		return
	}
	grads, gerr := w.st.ListCarryGraduations(ctx)
	if gerr != nil {
		log.Printf("[watchlist] list graduations: %v (graduation 腿保留旧集)", gerr)
	}
	combos, cerr := w.st.LoadXAssetAllowCombos(ctx, "live")
	if cerr != nil {
		log.Printf("[watchlist] load combos: %v (graduation 腿保留旧集)", cerr)
	}
	w.mu.Lock()
	w.rows = rows
	if gerr == nil && cerr == nil {
		w.grad = gradLegBases(grads, combos)
	}
	w.mu.Unlock()
}

// gradLegBases graduation 腿 → CEX 基币集（纯函数，D-542 S3①）：
// carry_graduations pending/approved 的 USDT 尾 canonical + 白名单 combos CEX 侧 canonical。
// MT canonical（XAUUSD 无 USDT 尾）天然不命中。
func gradLegBases(grads []pgstore.CarryGraduation, combos []pgstore.XAssetAllowRow) map[string]bool {
	out := map[string]bool{}
	add := func(canon, venue string) {
		if base := cexPairBase(canon); base != "" && isCexVenueKey(venue) {
			out[base] = true
		}
	}
	for _, g := range grads {
		if g.Status != "pending" && g.Status != "approved" {
			continue
		}
		add(g.Symbol, g.VenueA)
		add(g.Symbol, g.VenueB)
	}
	for _, c := range combos {
		add(c.CanonicalBuy, c.VenueBuy)
		add(c.CanonicalSell, c.VenueSell)
	}
	return out
}

// cexPairBase CEX pair canonical → 基币（"SKLUSDT"→"SKL"、"BTC-USDT-SWAP"→"BTC"、"CL_USDT"→"CL"）。
func cexPairBase(canon string) string {
	for _, suf := range []string{"-USDT-SWAP", "_USDT", "USDT"} {
		if b := strings.TrimSuffix(canon, suf); b != canon && b != "" {
			return b
		}
	}
	return ""
}

// isCexVenueKey venue_key venue 段=CEX（binance/okx/gate；空 venue_b=单边 spot_carry 结构收编）。
func isCexVenueKey(venueKey string) bool {
	if venueKey == "" {
		return true // spot_carry 单边腿（venue_b NULL 属 CEX 侧结构）
	}
	v, _, _ := strings.Cut(venueKey, ":")
	switch v {
	case "binance", "okx", "gate":
		return true
	}
	return false
}

// newWatchlistSymbolProvider 构造+启动刷新循环（初始同步加载一次，best-effort）。
// D-469b S5：configKick 事件主路（SetWatchlist RPC 即发即载）+5min ticker 兜底。
func newWatchlistSymbolProvider(ctx context.Context, st *pgstore.Store, kick <-chan struct{}) *watchlistSymbolProvider {
	w := &watchlistSymbolProvider{st: st}
	w.refresh(ctx)
	safego.GoLoop("main:watchlist-provider", func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-kick:
				w.refresh(ctx)
			case <-ticker.C:
				w.refresh(ctx)
			}
		}
	})
	return w
}
