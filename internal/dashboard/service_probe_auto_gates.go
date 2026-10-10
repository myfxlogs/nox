// service_probe_auto_gates.go —— D-163 机审③premise 回放 + ④可执行性闸。
// term_dispersion：不用 basis 内嵌 min/max 数字（D-159 化石实证——55.4 是停更 12.7 天的
// 历史极值），改用「当下鲜 funding facts」重算跨 venue min/max；贡献腿（任何出过
// funding facts 的 venue）任一根 facts 超龄 → 前提已化石 → 硬伤拒。
// idle_funds：venue 权益快照佐证（快照缺/venue 解不出=歧义；eq<expected_cost=硬伤）。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// probePremiseSeg 单段 premise 判定。返回三态恰一非空。
func (s *Service) probePremiseSeg(ctx context.Context, p *pgstore.ProbeProposal, seg string) (reject, ambiguous, note string) {
	head := strings.TrimRight(strings.Fields(seg)[0], ":：")
	switch head {
	case "term_dispersion":
		return s.replayTermDispersion(ctx, p, seg)
	case "idle_funds":
		return s.replayIdleFunds(ctx, p, seg)
	default:
		return "", fmt.Sprintf("未知 premise 前缀 %q", head), ""
	}
}

// replayTermDispersion 跨 venue funding 离散度回放：鲜 facts 重算 min/max，
// 贡献腿陈旧/鲜腿<2/离散塌缩 → 硬伤拒（前提化石不可证，不拿真金试）。
func (s *Service) replayTermDispersion(ctx context.Context, p *pgstore.ProbeProposal, seg string) (reject, ambiguous, note string) {
	canonical := p.Contract.Canonical
	for _, tok := range strings.Fields(seg) {
		tok = strings.Trim(tok, ":：，,[]")
		if tok == "" || tok == "term_dispersion" || tok == "funding" ||
			strings.Contains(tok, "=") || strings.Contains(tok, "..") || strings.Contains(tok, "×") {
			continue
		}
		if !strings.EqualFold(tok, canonical) {
			return "", fmt.Sprintf("premise canonical %q ≠ 契约 %q", tok, canonical), ""
		}
	}
	facts, err := s.store.LatestFacts(ctx, "funding", "", canonical)
	if err != nil {
		return "", fmt.Sprintf("funding facts 查询失败: %v", err), ""
	}
	ivFacts, err := s.store.LatestFacts(ctx, "funding_interval", "", canonical)
	if err != nil {
		return "", fmt.Sprintf("funding_interval facts 查询失败: %v", err), ""
	}
	ivl := map[string]time.Duration{}
	for _, f := range ivFacts {
		if f.Value > 0 {
			ivl[f.Venue] = time.Duration(f.Value) * time.Second
		}
	}
	now := time.Now()
	var stale []string
	var min, max float64
	fresh := 0
	for _, f := range facts {
		bound := 2 * probeFundingDefaultInterval
		if v, ok := ivl[f.Venue]; ok {
			bound = 2 * v
		}
		if age := now.Sub(f.Ts); age > bound {
			stale = append(stale, fmt.Sprintf("%s %s", f.Venue, age.Round(time.Minute)))
			continue
		}
		if fresh == 0 || f.Value < min {
			min = f.Value
		}
		if fresh == 0 || f.Value > max {
			max = f.Value
		}
		fresh++
	}
	if len(stale) > 0 {
		return fmt.Sprintf("premise 化石：funding facts 贡献腿陈旧 [%s]", strings.Join(stale, ",")), "", ""
	}
	if fresh < 2 {
		return fmt.Sprintf("premise 重算不出：%s 鲜 funding 贡献腿 %d <2", canonical, fresh), "", ""
	}
	if max-min <= 0 {
		return fmt.Sprintf("premise 已灭：%s 跨 venue funding 离散塌缩（min=max=%.3f）", canonical, min), "", ""
	}
	return "", "", fmt.Sprintf("term_dispersion %s 鲜重算 min=%.3f max=%.3f legs=%d",
		canonical, min, max, fresh)
}

// replayIdleFunds 闲置资金佐证：venue 权益快照（acctMon）佐证本金可付 expected_cost。
// 快照缺/venue 键解不出=歧义（不猜）；快照在且 eq<cost=硬伤。
func (s *Service) replayIdleFunds(ctx context.Context, p *pgstore.ProbeProposal, seg string) (reject, ambiguous, note string) {
	var venue string
	freeClaim := -1.0
	for _, tok := range strings.Fields(seg) {
		tok = strings.Trim(tok, ":：，,")
		switch {
		case tok == "" || tok == "idle_funds" || strings.HasPrefix(tok, "idle="):
		case strings.HasPrefix(tok, "free="):
			fmt.Sscanf(strings.TrimPrefix(tok, "free="), "%g", &freeClaim)
		case venue == "":
			venue = tok
		}
	}
	if venue == "" {
		return "", fmt.Sprintf("idle_funds 段无可解 venue: %q", seg), ""
	}
	legType := keydomain.Parse(p.Contract.VenueKey).Type
	cands := []string{keydomain.Join(venue, legType), keydomain.Join(venue, "perp"), keydomain.Join(venue, "spot"), venue}
	seen := map[string]bool{}
	for _, vk := range cands {
		if seen[vk] {
			continue
		}
		seen[vk] = true
		eq, _, asOf, ok := s.AccountEquityUSDAt(vk)
		if !ok {
			continue
		}
		if age := time.Since(asOf); age > probeEquitySnapshotMaxAge {
			return fmt.Sprintf("idle_funds %s 权益快照陈旧 %s", vk, age.Round(time.Second)), "", ""
		}
		if eq < p.Contract.ExpectedCostUSD {
			return fmt.Sprintf("idle_funds %s 权益 $%.2f < expected_cost $%.2f", vk, eq, p.Contract.ExpectedCostUSD), "", ""
		}
		return "", "", fmt.Sprintf("idle_funds %s eq=$%.2f（premise free=%.1f）", vk, eq, freeClaim)
	}
	return "", fmt.Sprintf("idle_funds venue %q 权益快照缺", venue), ""
}

// probeLegExecutable ④可执行性：报价鲜帧+原生符号、universe TRADING（CEX）、
// venue 流已知则须在线、perp 腿自身 funding facts 鲜。全硬伤。
func (s *Service) probeLegExecutable(ctx context.Context, p *pgstore.ProbeProposal) string {
	c := p.Contract
	if s.qbus == nil {
		return "qbus 未装配（无报价证据）"
	}
	q, ok := s.qbus.Latest(c.Canonical, c.VenueKey)
	if !ok || q.Mid() <= 0 || q.VenueSymbol == "" {
		return fmt.Sprintf("%s@%s 无鲜报价/原生符号", c.Canonical, c.VenueKey)
	}
	if s.freshTTL > 0 && time.Since(q.Time) > s.freshTTL {
		return fmt.Sprintf("%s@%s 报价陈旧 %s", c.Canonical, c.VenueKey, time.Since(q.Time).Round(time.Second))
	}
	vk := keydomain.Parse(c.VenueKey)
	if !vk.IsMT() { // CEX 腿：universe 登记 + TRADING。
		uni, err := s.store.ListCexUniverse(ctx)
		if err != nil {
			return fmt.Sprintf("universe 查询失败: %v", err)
		}
		row, found := uni[vk.Venue+"|"+q.VenueSymbol]
		if !found {
			return fmt.Sprintf("%s@%s 不在 universe（%s）", c.Canonical, c.VenueKey, q.VenueSymbol)
		}
		if row.Status != "TRADING" {
			return fmt.Sprintf("%s@%s universe 状态 %s≠TRADING", c.Canonical, c.VenueKey, row.Status)
		}
		if vk.Type == "perp" { // perp 腿自身 funding facts 鲜（同回放界）。
			facts, err := s.store.LatestFacts(ctx, "funding", vk.Venue, c.Canonical)
			if err != nil {
				return fmt.Sprintf("腿 funding facts 查询失败: %v", err)
			}
			if len(facts) == 0 {
				return fmt.Sprintf("%s@%s 无 funding facts（perp 腿证据缺）", c.Canonical, c.VenueKey)
			}
			ivFacts, err := s.store.LatestFacts(ctx, "funding_interval", vk.Venue, c.Canonical)
			if err != nil {
				return fmt.Sprintf("腿 funding_interval 查询失败: %v", err)
			}
			bound := 2 * probeFundingDefaultInterval
			if len(ivFacts) > 0 && ivFacts[0].Value > 0 {
				bound = 2 * time.Duration(ivFacts[0].Value) * time.Second
			}
			if age := time.Since(facts[0].Ts); age > bound {
				return fmt.Sprintf("%s@%s 腿 funding facts 陈旧 %s", c.Canonical, c.VenueKey, age.Round(time.Minute))
			}
		}
	}
	if s.streamOnlineFn != nil { // 流态已知 → 在线硬要求；未知键靠报价鲜帧（CEX 不登流态表）。
		if on, known := s.streamOnlineFn()[c.VenueKey]; known && !on {
			return fmt.Sprintf("venue 流离线: %s", c.VenueKey)
		}
	}
	return ""
}
