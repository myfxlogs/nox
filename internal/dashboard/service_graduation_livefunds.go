// service_graduation_livefunds.go —— D-542 S1/S2 装配层 + D-549 S1 统一读口接线。
// 口径（提示词 v2）：流主源（私有 WS 喂 bus.BalanceLedger）→ 干净直读零 REST；
// 脏/空 → 一发 REST 校准（复用 CEXAccountQuerier 经代理链，30s 缓存）→ 回写账本；
// REST 也败 → 快照表兜底（D-549：带龄注记禁静默，「快照+墙钟窗」判定路径根除）。
// D-549 S1：读口本体收编进 internal/capitalview（本文件只留装配+腿池映射）。
package dashboard

import (
	"context"
	"log"
	"strings"
	"time"

	"arb/internal/capitalview"
	"arb/internal/keydomain"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// capitalView 统一资金读口（D-549 S1）——惰性装配一次（共享实例保校准单飞）；
// liveMu 守构造，SetBalanceLedger/测试注入后重建。
func (s *Service) capitalView() *capitalview.View {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.capView != nil {
		return s.capView
	}
	s.capView = capitalview.New(capitalview.Deps{
		Ledger:     s.balLedger,
		RestTTL:    s.liveRestTTL,
		QuerierFor: s.livePoolQuerier,
		Snapshots: func(ctx context.Context) ([]pgstore.VenueAccountLatest, error) {
			if s.gradStore == nil && s.store == nil {
				return nil, nil // 部分构造 Service（测试字面量）nil 安全
			}
			return s.gradDB().LatestVenueAccountSnapshots(ctx)
		},
	})
	return s.capView
}

// legPool 单腿池求值（D-542 统一入口 → D-549 起经 capitalview.Live）：
// CEX 腿→活账本（干净直读→脏/空 REST 校准）；MT 腿/校准失败 → 快照兜底
// （带龄注记；调用方快照龄闸只对非 live 池生效）。snaps=调用方预取面（nil=自取）。
func (s *Service) legPool(ctx context.Context, snaps []pgstore.VenueAccountLatest, venueKey string) (allocPool, bool) {
	f, ok := s.capitalView().LiveSnaps(ctx, venueKey, snaps)
	if !ok {
		return allocPool{}, false
	}
	p := allocPool{key: f.VenueKey, equity: f.Equity, currency: f.Currency, at: f.At, live: f.Live}
	if f.Src != capitalview.SrcSnap {
		p.src = f.Src // "ws"/"rest"（snap 行 src 留空=poolSource 映射 "snap"）
	}
	if f.HasAvailable {
		avail := f.Available
		p.free = &avail
	}
	return p, true
}

// liveRestTTL 校准缓存期（字段注入测试可缩短；缺省 liveRestTTL）。
func (s *Service) liveRestTTL() time.Duration {
	if s.liveRestTTLDur > 0 {
		return s.liveRestTTLDur
	}
	return capitalview.DefaultRestTTL
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
