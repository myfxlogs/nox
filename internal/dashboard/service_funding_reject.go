// service_funding_reject.go —— D-560 S2b：近 6h 资金拒事实透出（pendingEntries×
// ListXAssetAllowCombos 共用）。一次批量查询建 canonical|venueBuy|venueSell→最新
// 资金拒 reason 映射（禁 N+1）；errclass.IsFundingRejectMsg 判据单源。
package dashboard

import (
	"context"
	"time"

	"arb/internal/combo"
	"arb/internal/errclass"
	"arb/internal/store/pgstore"
)

// RejectBreakerReader D-560 S3：熔断态读口（autotrade.Engine 满足；接口注入防循环依赖，
// 同 XAssetReloader 先例）。nil=未接线 → 三字段零值、Resume RPC 返错。
type RejectBreakerReader interface {
	BreakerSnapshot() map[string]errclass.RejectBreakerInfo
	ResumeBreaker(dedupKey string) int
}

// SetRejectBreaker 熔断读口接线（setup：engine 装配后注）。
func (s *Service) SetRejectBreaker(r RejectBreakerReader) { s.rejectBreaker = r }

// breakerDedupKeys 组合 → 候选 dedupKey 集（opp.ID=combo.Key(strategy,canon,vb,vs)；
// canonical 双侧都试——intent 记的是机会 canonical 可能落在任一腿）。
func breakerDedupKeys(c pgstore.XAssetComboAdminRow) []string {
	var out []string
	for _, strategy := range []string{"XASSET_CARRY", "FUNDING_HEDGE"} {
		for _, canon := range []string{c.CanonicalBuy, c.CanonicalSell} {
			out = append(out, combo.Key(strategy, canon, c.VenueBuy, c.VenueSell))
		}
	}
	return out
}

// breakerFor 组合行查活动熔断条目（dedupKey 前缀匹配；多条取最早 resumeAt）。
func (s *Service) breakerFor(c pgstore.XAssetComboAdminRow) *errclass.RejectBreakerInfo {
	if s.rejectBreaker == nil {
		return nil
	}
	snap := s.rejectBreaker.BreakerSnapshot()
	var best *errclass.RejectBreakerInfo
	for _, dk := range breakerDedupKeys(c) {
		for k, info := range snap {
			if len(k) > len(dk) && k[:len(dk)+1] == dk+"|" {
				cp := info
				if best == nil || cp.ResumeAt.Before(best.ResumeAt) {
					best = &cp
				}
			}
		}
	}
	return best
}

// fundingRejectFacts 近6h FAILED autotrade_logs → 每组合最新资金拒 reason。
// 查询失败=透出降级（返空表，不阻塞面板主行——同 snaps 读败先例）。
func (s *Service) fundingRejectFacts(ctx context.Context) map[string]string {
	logs, err := s.gradDB().ListRecentFailedAutotradeLogs(ctx, 6*time.Hour)
	if err != nil {
		return nil
	}
	m := make(map[string]string, len(logs))
	for _, l := range logs { // ORDER BY ts DESC → 首个命中即最新
		if !errclass.IsFundingRejectMsg(l.Reason) {
			continue
		}
		k := l.Canonical + "|" + l.VenueBuy + "|" + l.VenueSell
		if _, ok := m[k]; !ok {
			m[k] = l.Reason
		}
	}
	return m
}

// fundingRejectFor 组合行查拒事实——canonical 双键都试（intent.Canonical 记的是
// 机会 canonical，可能是买或卖腿侧）。
func fundingRejectFor(facts map[string]string, canonicalBuy, canonicalSell, venueBuy, venueSell string) (string, bool) {
	for _, c := range []string{canonicalBuy, canonicalSell} {
		if r, ok := facts[c+"|"+venueBuy+"|"+venueSell]; ok {
			return r, true
		}
	}
	return "", false
}

// rejectReasonText 透出文案=拒文截断+解法行（命中才追加，非资金拒文 FundingOptionsText 自返 ""）。
func rejectReasonText(reason string) string {
	acct := "" // D-563 S5：账户名=拒文腿键裸名（无别名表在 dashboard 域）
	if m := errclass.RejectLegRe.FindStringSubmatch(reason); m != nil {
		acct = m[1]
	}
	return errclass.TruncateNote(reason, 120) + errclass.FundingOptionsText(reason, acct)
}
