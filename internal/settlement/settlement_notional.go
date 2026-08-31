package settlement

import (
	"context"
	"time"

	"arb/internal/notional"
	"arb/internal/store/pgstore"
)

// computeNotionalUSD Σ OPEN FILLED 腿 LegUSD（CS 经归一 + fx 经 USDPer）。
// 返回 (notionalUSD, fxSkipped)：任一腿 fx 不可得 → fxSkipped=true（notional 可能部分算）。
func computeNotionalUSD(ctx context.Context, deps Deps, legs []pgstore.PositionLeg, canonical string) (float64, bool) {
	quoteCcy := notional.QuoteCcy(canonical)
	total := 0.0
	skipped := false
	for _, l := range legs {
		if l.Phase != "OPEN" || l.Status != "FILLED" {
			continue
		}
		if l.PriceActual == nil || *l.PriceActual <= 0 || l.Qty <= 0 {
			continue
		}
		cs := 1.0 // CEX 腿 CS=1
		if c, ok, _ := deps.ContractSizeByCanonical(ctx, l.Venue, canonical); ok && c > 0 {
			cs = c
		}
		fx := 1.0
		if quoteCcy != "" && quoteCcy != "USD" {
			rate, ok := deps.USDPer(ctx, quoteCcy, l.Ts)
			if !ok {
				skipped = true
				continue
			}
			fx = rate // USDPer 返回 USD per quoteCcy，直接作 fx（D-087 返修③：删双重求逆）
		}
		total += notional.LegUSD(l.Qty, cs, *l.PriceActual, fx)
	}
	return total, skipped
}

// computeHoldDays (closed_at − opened_at)/24h；opened_at NULL → min(OPEN FILLED 腿 ts)。
func computeHoldDays(p *pgstore.Position, legs []pgstore.PositionLeg) float64 {
	if p.ClosedAt == nil {
		return 0
	}
	openedAt := p.OpenedAt
	if openedAt == nil {
		var minTs time.Time
		for _, l := range legs {
			if l.Phase == "OPEN" && l.Status == "FILLED" {
				if minTs.IsZero() || l.Ts.Before(minTs) {
					minTs = l.Ts
				}
			}
		}
		if !minTs.IsZero() {
			openedAt = &minTs
		}
	}
	if openedAt == nil {
		return 0
	}
	return p.ClosedAt.Sub(*openedAt).Hours() / 24
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
// 与 verification/pgstore 同源纯函数，本包不依赖 autotrade（避免环依赖）。
func canonicalFromOppID(oppID string) string {
	// 剥离旧 tick 尾时间戳
	ck := oppID
	if idx := lastIndexColon(oppID); idx >= 0 {
		tail := oppID[idx+1:]
		if isAllDigits(tail) {
			ck = oppID[:idx]
		}
	}
	parts := splitColon(ck)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func lastIndexColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func splitColon(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
