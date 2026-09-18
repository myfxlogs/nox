// backfill.go D-341 S5：价差史深回填（-backfill 模式）——
// ticks(3001157 exSym) 5min 桶 × binance klines 5m（公开端点经既有 proxy）对齐 →
// 写 fin_xasset_monitor 历史行（src='backfill'）。深度=Exness ticks 保留窗（现 5d，诚实边界，
// 不引入代理价差源）。回填行仅 entry_spread 有效（funding/tracking 列 0 填充满足 NOT NULL；
// noise/floor NULL 由 live 轨自长）。缺桶（一侧无数据）跳过+计数。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"arb/internal/httpx"
	"arb/internal/store/pgstore"
)

// kline binance fapi /fapi/v1/klines 行（5m）。
type kline struct {
	OpenTime time.Time
	Close    float64
}

// tickBucket ticks 5min 桶（mid 均值）。
type tickBucket struct {
	Bucket time.Time
	Mid    float64
}

// fetchKlines5m 拉 binance 5m klines（公开端点，limit≤1500）。
func fetchKlines5m(ctx context.Context, hc *http.Client, proxyURL, symbol string, startTime time.Time) ([]kline, error) {
	url := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=5m&startTime=%d&limit=1500",
		proxyURL, symbol, startTime.UnixMilli())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("klines http %d: %s", resp.StatusCode, string(body))
	}
	var raw [][]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]kline, 0, len(raw))
	for _, r := range raw {
		if len(r) < 5 {
			continue
		}
		var k kline
		if ot, ok := r[0].(float64); ok {
			k.OpenTime = time.UnixMilli(int64(ot))
		}
		if c, ok := r[4].(string); ok {
			fmt.Sscanf(c, "%f", &k.Close)
		}
		if !k.OpenTime.IsZero() && k.Close > 0 {
			out = append(out, k)
		}
	}
	return out, nil
}

// alignSpread T5 纯函数：ticks 桶 × klines 5min 对齐 → (esp 序列, 缺桶计数)。
// 对齐键=5min 桶开盘时刻；任一侧缺桶跳过+计数（诚实边界：不对齐不造数）。
func alignSpread(ticks []tickBucket, klines []kline) ([]float64, int) {
	kByBucket := map[int64]float64{}
	for _, k := range klines {
		kByBucket[k.OpenTime.Unix()/(5*60)] = k.Close
	}
	var out []float64
	miss := 0
	for _, tb := range ticks {
		b := tb.Bucket.Unix() / (5 * 60)
		if k, ok := kByBucket[b]; ok && k > 0 && tb.Mid > 0 {
			out = append(out, (k-tb.Mid)/tb.Mid*10000)
		} else {
			miss++
		}
	}
	sort.Float64s(out)
	return out, miss
}

// RunBackfill D-341 S5 主流程：Exness ticks 桶化 × binance klines 对齐 → 逐行写 src='backfill'。
func RunBackfill(ctx context.Context, st *pgstore.Store, hc *http.Client, proxyURL, exnessVenue, sym, pair, exSym string, since time.Time) (int, error) {
	rows, err := st.Pool().Query(ctx,
		`SELECT ts, (bid+ask)/2 FROM ticks WHERE venue=$1 AND symbol=$2 AND ts > $3 ORDER BY ts`, exnessVenue, exSym, since)
	if err != nil {
		return 0, fmt.Errorf("ticks query: %w", err)
	}
	bucketSum, bucketCnt := map[int64]float64{}, map[int64]int{}
	for rows.Next() {
		var ts time.Time
		var mid float64
		if err := rows.Scan(&ts, &mid); err != nil {
			rows.Close()
			return 0, err
		}
		b := ts.Unix() / (5 * 60)
		bucketSum[b] += mid
		bucketCnt[b]++
	}
	rows.Close()
	ticks := make([]tickBucket, 0, len(bucketSum))
	for b, sum := range bucketSum {
		ticks = append(ticks, tickBucket{Bucket: time.Unix(b*(5*60), 0), Mid: sum / float64(bucketCnt[b])})
	}
	klines, err := fetchKlines5m(ctx, hc, proxyURL, sym, since)
	if err != nil {
		return 0, fmt.Errorf("klines: %w", err)
	}
	esps, miss := alignSpread(ticks, klines)
	written := 0
	for _, esp := range esps {
		rec := &pgstore.XassetMonitor{Pair: pair, EntrySpreadBps: esp, Src: "backfill"}
		if err := st.InsertXassetMonitor(ctx, rec); err != nil {
			return written, fmt.Errorf("insert backfill row: %w", err)
		}
		written++
	}
	log.Printf("xasset-monitor backfill: pair=%s written=%d skipped(miss bucket)=%d ticksBuckets=%d klines=%d",
		pair, written, miss, len(ticks), len(klines))
	return written, nil
}

// runBackfillMode D-341 S5：三对逐对回填（fail-open 同 live 轨 9-16 裁定）。
func runBackfillMode(ctx context.Context) error {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		return fmt.Errorf("DB_DSN 未设置")
	}
	proxyURL := os.Getenv("BINANCE_PROXY_URL")
	if proxyURL == "" {
		return fmt.Errorf("BINANCE_PROXY_URL 未设置")
	}
	exnessVenue := os.Getenv("ARB_XASSET_EXNESS_VENUE")
	if exnessVenue == "" {
		exnessVenue = "3001157"
	}
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect pg: %w", err)
	}
	defer st.Close()
	hc := httpx.Client()
	since := time.Now().Add(-5 * 24 * time.Hour) // Exness ticks 保留窗（诚实边界）
	var firstErr error
	for _, p := range monitorPairs() {
		if _, err := RunBackfill(ctx, st, hc, proxyURL, exnessVenue, p.sym, p.pair, p.exSym, since); err != nil {
			log.Printf("xasset-monitor backfill: pair=%s 失败: %v", p.pair, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
