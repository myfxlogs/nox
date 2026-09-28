// service_capital_map_venues.go —— D-546 R1 扩面：okx（unified 子账本拆分）+
// gate（/spot/accounts+/futures/usdt/accounts 并读）入 CapitalMap 三档树。
// 数据面=reconcile.CEXAccountQuerier.Availability（D-547 三档采集已建）；
// 立即可用档优先活账本 legPool（与 sizing 基准同源），子面/锁定档=REST 实拉
// （60s 进程缓存与 binance gaps 同节律）；面败/未配置=标缺不猜零。
package dashboard

import (
	"context"
	"log"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// venueAvailFetcher venue 钱包树实拉通道（reconcile.CEXAccountQuerier.Availability
// 实现；测试注入 stub）。
type venueAvailFetcher interface {
	Fetch(ctx context.Context) (*reconcile.VenueAvailability, error)
}

type querierAvail struct{ q *reconcile.CEXAccountQuerier }

func (a querierAvail) Fetch(ctx context.Context) (*reconcile.VenueAvailability, error) {
	return a.q.Availability(ctx)
}

// venueAvailability venue 全钱包树读口：60s 缓存（capGapsTTL 同节律）+
// fetcher 懒装配（real spot/perp 行凭据，sim 行 fail-closed）。
func (s *Service) venueAvailability(ctx context.Context, venue string) *reconcile.VenueAvailability {
	s.capAvailMu.Lock()
	defer s.capAvailMu.Unlock()
	if c, ok := s.capAvailCache[venue]; ok && time.Since(s.capAvailAt[venue]) < s.capGapsTTL() {
		return c
	}
	f, ok := s.venueAvailFor(ctx, venue)
	if !ok || f == nil {
		return nil
	}
	va, err := f.Fetch(ctx)
	if va == nil {
		return nil
	}
	if err != nil {
		log.Printf("[capital-map] %s availability: %v（降级入缓存，窗口后重试）", venue, err)
	}
	if s.capAvailCache == nil {
		s.capAvailCache = map[string]*reconcile.VenueAvailability{}
		s.capAvailAt = map[string]time.Time{}
	}
	s.capAvailCache[venue] = va
	s.capAvailAt[venue] = time.Now()
	return va
}

// venueAvailFor 测试缝优先（capAvailFor 注入）；生产=real 配置行建 querier。
func (s *Service) venueAvailFor(ctx context.Context, venue string) (venueAvailFetcher, bool) {
	if s.capAvailFor != nil {
		return s.capAvailFor(ctx, venue)
	}
	return s.newVenueAvailability(ctx, venue), true
}

// newVenueAvailability 生产装配：okx=任一 real 行（unified 账户任意交易配置可询）；
// gate=perp 行凭据（futures key 权限集，spot 权限缺席如实降级）。
func (s *Service) newVenueAvailability(ctx context.Context, venue string) venueAvailFetcher {
	cfgs, err := s.gradDB().ListExchangeConfigs(ctx)
	if err != nil {
		log.Printf("[capital-map] list exchange configs: %v", err)
		return nil
	}
	live := realOnlyConfigs(cfgs, &s.liveSimWarned, "capital-map")
	for i := range live {
		c := &live[i]
		if c.Venue != venue {
			continue
		}
		if venue == "gate" && c.VenueType != "perp" {
			continue // gate 树主键=perp 凭据行
		}
		return querierAvail{q: reconcile.NewCEXAccountQuerier(
			c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase,
			resolveExchangeBaseURL(*c), c.Website, c.Simulated)}
	}
	log.Printf("[capital-map] 无 %s real 配置行——钱包树面如实标缺", venue)
	return nil
}

// availWallet 按 wallet 名取节点（nil-safe）。
func availWallet(va *reconcile.VenueAvailability, wallet string) *reconcile.AvailabilityNode {
	if va == nil {
		return nil
	}
	for _, n := range va.Nodes {
		if n != nil && n.Wallet == wallet {
			return n
		}
	}
	return nil
}

// availLiveNode 立即可用节点：venueKey 非空=优先活账本 legPool（sizing 同源）；
// venueKey 空=纯 REST 面（gate:spot 无私有流且腿池键回退会串到 gate:perp 错池，
// 禁走 legPool）。账本空回退 REST 面——判据按 wallet 节点存在性（REST 答了
// 真 $0 须出诚实零节点，非误标三径全空；面败 Detail+全零=标缺，同 walletLocked）。
func (s *Service) availLiveNode(ctx context.Context, snaps []pgstore.VenueAccountLatest, venueKey, label string, restNode *reconcile.AvailabilityNode, restSrc string) *v1.CapitalMapNode {
	if venueKey != "" {
		if p, ok := s.legPool(ctx, snaps, venueKey); ok && p.free != nil {
			return capNode(label, *p.free, p.currency, capSrcNote(p), p.at)
		}
	}
	if restNode == nil {
		return capMissing(label, "活账本/REST/快照三径全空（面未出 "+restSrc+"）")
	}
	if restNode.Detail != "" && restNode.AvailableUSD == 0 && restNode.TransferableUSD == 0 && restNode.LockedUSD == 0 {
		return capMissing(label, restNode.Detail)
	}
	return capNode(label, restNode.AvailableUSD, "USDT", restSrc, restNode.At)
}

// walletLocked 锁定节点（面败 Detail 透出为缺因；零值仍出诚实节点）。
func walletLocked(va *reconcile.VenueAvailability, wallet, label, src string) *v1.CapitalMapNode {
	n := availWallet(va, wallet)
	if n == nil {
		return capMissing(label, "面未出（"+src+"）")
	}
	if n.Detail != "" && n.AvailableUSD == 0 && n.TransferableUSD == 0 && n.LockedUSD == 0 {
		return capMissing(label, n.Detail)
	}
	return capNode(label, n.LockedUSD, "USDT", src, n.At)
}

// walletTransfer 可转可用节点（同 walletLocked 降级语义）。
func walletTransfer(va *reconcile.VenueAvailability, wallet, label, src string) *v1.CapitalMapNode {
	n := availWallet(va, wallet)
	if n == nil {
		return capMissing(label, "面未出（"+src+"）")
	}
	if n.Detail != "" && n.AvailableUSD == 0 && n.TransferableUSD == 0 && n.LockedUSD == 0 {
		return capMissing(label, n.Detail)
	}
	return capNode(label, n.TransferableUSD, "USDT", src, n.At)
}
