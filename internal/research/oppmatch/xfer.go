// xfer.go —— 跨 venue 机会差调拨提案（业主令 10-14「钱跟利走」配套）。
// 同资产跨所 earn APR 差超阈+源池有闲量→fin_condition_gap 投资人侧请求行。
// 系统只提案永不执行划转（充提币=业主域）；比价源缺一方/陈旧 fail-closed 不比；
// 阈值 app_config xfer_apr_diff_pct（pp 点，缺省 5）可改不硬编。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// xferTemplateID 调拨提案模板标识（fin_condition_gap.template_id 非 FK——哨兵值即可）。
const xferTemplateID = "cross_venue_xfer"

// xferCCYs 先行域=USDT（票面口径；扩列=改此集+锚）。
var xferCCYs = map[string]bool{"USDT": true}

// xferScan 并入扫描拍（零新定时器）。同对同向 gap 行 UNIQUE 幂等零重推；
// 业主收口（fulfilled）后差仍存→RequestConditionGap 复推（D-196② 既有语义）。
func xferScan(ctx context.Context, st *pgstore.Store) {
	th := 5.0
	if v, ok, err := st.AppConfigFloat(ctx, "xfer_apr_diff_pct"); err == nil && ok && v > 0 {
		th = v
	}
	aprs, err := st.LatestCexEarnAPRs(ctx)
	if err != nil {
		log.Printf("[xfer] earn apr 读败不比: %v", err)
		return
	}
	pools, err := st.ExplorerIdleFunds(ctx, time.Now())
	if err != nil {
		log.Printf("[xfer] idle 读败不比: %v", err)
		return
	}
	// venue→apr（每 ccy）；>24h 陈旧行剔——比价源不新鲜不比。
	best := map[string]map[string]float64{}
	for _, r := range aprs {
		if !xferCCYs[r.Ccy] || time.Since(r.Ts) > 24*time.Hour {
			continue
		}
		if best[r.Ccy] == nil {
			best[r.Ccy] = map[string]float64{}
		}
		if r.APR > best[r.Ccy][r.Venue] {
			best[r.Ccy][r.Venue] = r.APR
		}
	}
	// venue→闲池权益（每 ccy；Idle=睡钱标——deployed 不动只提闲量）。
	idleEq := map[string]map[string]float64{}
	for _, p := range pools {
		if p.Ghost || !p.Idle || !xferCCYs[p.Currency] {
			continue
		}
		eq, ok := livePoolEquity(ctx, p.Venue, p.VenueType, p.Equity) // D-604 P2③ 无流池实拉
		if !ok || eq <= 0 {
			continue
		}
		if idleEq[p.Currency] == nil {
			idleEq[p.Currency] = map[string]float64{}
		}
		idleEq[p.Currency][p.Venue] += p.Equity
	}
	for ccy, vs := range best {
		// dst=APR 最高所；src=有 APR 行（比价源齐=fail-closed 前提）+闲池非零。
		dst, dstAPR := "", -1.0
		for v, a := range vs {
			if a > dstAPR {
				dst, dstAPR = v, a
			}
		}
		for src, srcAPR := range vs {
			// APR 列=小数（0.01=1%）；阈值 xfer_apr_diff_pct=pp 点——差先×100 再比。
			if src == dst || srcAPR >= dstAPR || (dstAPR-srcAPR)*100 < th {
				continue
			}
			amt := idleEq[ccy][src]
			if amt <= 0 {
				continue
			}
			subject := fmt.Sprintf("%s:%s>%s", ccy, src, dst)
			id, err := st.OpenConditionGap(ctx, &pgstore.ConditionGap{
				TemplateID: xferTemplateID, Kind: "transfer", Subject: subject,
				RequestText: fmt.Sprintf(
					"跨所收益差超阈：建议划转 %.2f %s %s→%s（earn APR %.2f%%→%.2f%%，差 %.1fpt）——划转属业主域，系统只提案不执行",
					amt, ccy, src, dst, srcAPR*100, dstAPR*100, (dstAPR-srcAPR)*100),
			})
			if err != nil {
				log.Printf("[xfer] open gap %s: %v", subject, err)
				continue
			}
			if err := st.RequestConditionGap(ctx, id); err != nil {
				log.Printf("[xfer] request %s: %v", subject, err)
			}
		}
	}
}
