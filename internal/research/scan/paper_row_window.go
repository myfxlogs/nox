package scan

import (
	"context"
	"time"

	"arb/internal/data/fact"
)

// paperIntervalQuerier 行窗判定所需窄读面（PaperStore/PaperWindowStore 同签名满足）。
type paperIntervalQuerier interface {
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
}

// paperRowSettleWindow 行结算窗=max(双腿)：funding 腿 iv（facts 实读，缺/非正→回退
// MT 腿恒 24h UTC 日界）与 MT 腿 24h 日界取大。accrue 段「零边界」告警阈与
// evalPaperRow stalled 判共用此口径（D-028 尾巴 b：原 accrue 硬编码 48h 改同源）。
func paperRowSettleWindow(ctx context.Context, st paperIntervalQuerier, cexV, rawCex string) time.Duration {
	iv := 24 * time.Hour
	if ivF, err := st.LatestFacts(ctx, fact.KindFundingInterval, cexV, rawCex); err == nil && len(ivF) > 0 && ivF[0].Value > 0 {
		if fiv := time.Duration(int64(ivF[0].Value)) * time.Second; fiv > iv {
			iv = fiv
		}
	}
	return iv
}
