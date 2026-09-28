// service_graduation_livefunds.go —— D-542 S1/S2：腿池实拉活账本评估。
// 口径（提示词 v2）：流主源（私有 WS 喂 bus.BalanceLedger）→ 干净直读零 REST；
// 脏/空 → 一发 REST 校准（复用 CEXAccountQuerier 经代理链，30s 缓存）→ 回写账本；
// REST 也败 → 快照表兜底（快照龄闸只作用于这条兜底路径）。MT 腿恒走快照（无私有流）。
package dashboard

import (
	"context"
	"log"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/keydomain"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// liveRestTTL REST 校准缓存期（业主口径「实拉=弹窗触发+30s 缓存」）。
const liveRestTTL = 30 * time.Second

// livePoolOf 活账本条目 → 池行（available 恒实采→free 直指，无「净值回退」歧义）。
func livePoolOf(e bus.BalanceEntry) allocPool {
	avail := e.Available
	return allocPool{
		key: e.VenueKey, equity: e.Equity, free: &avail, currency: e.Currency,
		at: e.At, live: true, src: e.Src,
	}
}

// legPool 单腿池求值（D-542 统一入口）：CEX 腿→活账本（干净直读→脏/空 REST 校准）；
// MT 腿/校准失败 → allocPoolOf 快照兜底（调用方快照龄闸只对非 live 池生效）。
func (s *Service) legPool(ctx context.Context, snaps []pgstore.VenueAccountLatest, venueKey string) (allocPool, bool) {
	if s.balLedger != nil && isCexVenue(venueKey) {
		k := keydomain.Parse(venueKey)
		for _, key := range livePoolKeys(k.Venue, k.Type) {
			if e, ok := s.balLedger.Get(key, s.liveRestTTL(), time.Now()); ok {
				return livePoolOf(e), true
			}
		}
		if p, ok := s.calibratePool(ctx, venueKey); ok {
			return p, true
		}
	}
	return allocPoolOf(snaps, venueKey)
}

// liveRestTTL 校准缓存期（字段注入测试可缩短；缺省 liveRestTTL）。
func (s *Service) liveRestTTL() time.Duration {
	if s.liveRestTTLDur > 0 {
		return s.liveRestTTLDur
	}
	return liveRestTTL
}

// isCexVenue 腿 venue 是否 CEX（活账本通道覆盖三所；MT venue 段=数字账户号不走此径）。
func isCexVenue(venueKey string) bool {
	switch keydomain.Parse(venueKey).Venue {
	case "binance", "okx", "gate":
		return true
	}
	return false
}

// livePoolKeys 活账本候选键序（与 allocPoolOf 回退链同构：精确→unified→perp）。
// venueKey 撞键 #id 后缀剥除——账本键=裸 canonical 池键（账户级口径）。
func livePoolKeys(bare, legType string) []string {
	if i := strings.IndexByte(legType, '#'); i >= 0 {
		legType = legType[:i]
	}
	out := make([]string, 0, 3)
	for _, t := range []string{legType, "unified", "perp"} {
		if t == "" {
			continue
		}
		k := keydomain.Join(bare, t)
		dup := false
		for _, o := range out {
			if o == k {
				dup = true
			}
		}
		if !dup {
			out = append(out, k)
		}
	}
	return out
}

// calibratePool 脏/空路径：单飞互斥下一发 REST 校准（复用 CEXAccountQuerier 经代理，
// 防并发开窗风暴）→ 回写账本 src=rest（liveRestTTL 内复用）。败→false 走快照兜底。
func (s *Service) calibratePool(ctx context.Context, venueKey string) (allocPool, bool) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	// 获锁后复查——并发腿可能刚校准过同池（spot×perp 两腿同 venue 常见）。
	k := keydomain.Parse(venueKey)
	for _, key := range livePoolKeys(k.Venue, k.Type) {
		if e, ok := s.balLedger.Get(key, s.liveRestTTL(), time.Now()); ok {
			return livePoolOf(e), true
		}
	}
	q, poolKey, ok := s.livePoolQuerier(ctx, venueKey)
	if !ok {
		return allocPool{}, false
	}
	acct, err := q.Account(ctx)
	if err != nil {
		log.Printf("[live-funds] %s REST 校准失败: %v（快照兜底）", venueKey, err)
		return allocPool{}, false
	}
	avail := acct.FreeMargin
	if !(avail > 0) {
		avail = acct.Equity // venue 未供可用口径→净值回退（与 allocUsable 同语义）
	}
	at := time.Now()
	s.balLedger.Put(poolKey, avail, acct.Equity, acct.Currency, "rest", at)
	return livePoolOf(bus.BalanceEntry{
		VenueKey: poolKey, Available: avail, Equity: acct.Equity,
		Currency: acct.Currency, At: at, Src: "rest",
	}), true
}

// livePoolQuerier 腿 venue → CEXAccountQuerier（复用 account-monitor 同链：
// ListExchangeConfigs → realOnlyConfigs（sim 行 fail-closed）→ resolveExchangeBaseURL
// （代理通道内建——binance key 白名单 IP 必经代理）→ CexVenueKey 撞键归一）。
// 返回 (querier, 活账本池键, 命中)。okx 统一账户=任意 okx 交易配置可询，池键钉 okx:unified。
func (s *Service) livePoolQuerier(ctx context.Context, venueKey string) (reconcile.AccountQuerier, string, bool) {
	if s.liveQuerierFor != nil {
		q, poolKey, ok := s.liveQuerierFor(ctx, venueKey)
		return q, poolKey, ok
	}
	k := keydomain.Parse(venueKey)
	bare, typ := k.Venue, k.Type
	if i := strings.IndexByte(typ, '#'); i >= 0 {
		typ = typ[:i]
	}
	cfgs, err := s.gradDB().ListExchangeConfigs(ctx)
	if err != nil {
		log.Printf("[live-funds] %s list exchange configs: %v（快照兜底）", venueKey, err)
		return nil, "", false
	}
	live := realOnlyConfigs(cfgs, &s.liveSimWarned, "live-funds")
	winners := cexKeyWinners(live)
	for i := range live {
		c := &live[i]
		if c.Venue != bare {
			continue
		}
		if bare != "okx" && c.VenueType != typ {
			continue // binance/gate 按 venue_type 精确配腿（spot 腿禁读 perp 池）
		}
		base := resolveExchangeBaseURL(*c)
		q := reconcile.NewCEXAccountQuerier(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Website, c.Simulated)
		q.SetKeyOverride(reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners))
		poolKey := keydomain.Join(bare, c.VenueType)
		if bare == "okx" {
			poolKey = "okx:unified" // 统一账户（快照行同口径钉法）
		}
		return q, poolKey, true
	}
	return nil, "", false
}
