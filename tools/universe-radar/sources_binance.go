// ---------- Binance（D-591 S6：四连 REST 退役——exchangeInfo 日缓存 + ticks/facts 读库） ----------
// 目录枚举 exchangeInfo=唯一保留 REST（24h 日缓存降频，目录数据低频变化非决策消费轮询）；
// premiumIndex→facts funding/funding_interval、ticker/24hr→facts vol24h（!ticker@arr
// 落库）、mark→ticks binance perp mid（≈mark 近似）。coverage=core 有效集 facts/ticks，
// 未覆盖符号诚实零值（annualize nil→资格判定跳过，禁猜值）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type binance struct {
	hc       *http.Client
	base     string // exchangeInfo 经 proxy（封禁防护同源）
	pool     *pgxpool.Pool
	cacheDir string // exchangeInfo 日缓存目录（测试注入 t.TempDir()）
}

func (b binance) Venue() string { return "binance" }

// binanceExchangeInfo exchangeInfo 响应+缓存时间戳。
type binanceExchangeInfo struct {
	FetchedAt time.Time `json:"fetched_at"`
	Symbols   []struct {
		Symbol         string  `json:"symbol"`
		BaseAsset      string  `json:"baseAsset"`
		QuoteAsset     string  `json:"quoteAsset"`
		ContractType   string  `json:"contractType"`
		UnderlyingType string  `json:"underlyingType"`
		Status         string  `json:"status"`
		OnboardDate    flexInt `json:"onboardDate"` // ms
	} `json:"symbols"`
}

// exchangeInfoCached 日缓存：新鲜（<24h）直用；过期/缺失拉一次并落盘；拉失败有
// 过期缓存 → 回落（目录低频变化，宁旧不炸整轮）。
func (b binance) exchangeInfoCached(ctx context.Context) (binanceExchangeInfo, error) {
	var out binanceExchangeInfo
	path := filepath.Join(b.cacheDir, "binance-exchangeinfo.json")
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &out) == nil {
		if time.Since(out.FetchedAt) < 24*time.Hour {
			return out, nil
		}
	}
	fresh, err := b.exchangeInfoFetch(ctx)
	if err != nil {
		if out.FetchedAt.IsZero() {
			return out, fmt.Errorf("exchangeInfo: %w", err)
		}
		log.Printf("[universe-radar] binance exchangeInfo 刷新失败，回落过期缓存（%s）: %v",
			out.FetchedAt.Format(time.RFC3339), err)
		return out, nil
	}
	out = fresh
	if raw, err := json.Marshal(out); err == nil {
		_ = os.MkdirAll(b.cacheDir, 0o755)
		_ = os.WriteFile(path, raw, 0o644)
	}
	return out, nil
}

func (b binance) exchangeInfoFetch(ctx context.Context) (binanceExchangeInfo, error) {
	var ei binanceExchangeInfo
	if err := getJSON(ctx, b.hc, b.base+"/fapi/v1/exchangeInfo", &ei); err != nil {
		return ei, err
	}
	ei.FetchedAt = time.Now()
	return ei, nil
}

func (b binance) Scan(ctx context.Context) ([]Row, error) {
	ei, err := b.exchangeInfoCached(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(ei.Symbols))
	idx := make(map[string]int)
	for _, s := range ei.Symbols {
		// 仅收 USDT 本位 perp（contractType 含 PERPETUAL、quoteAsset=USDT）
		if !strings.Contains(s.ContractType, "PERPETUAL") || s.QuoteAsset != "USDT" {
			continue
		}
		idx[s.Symbol] = len(rows)
		rows = append(rows, Row{
			Symbol: s.Symbol, Base: s.BaseAsset, Class: s.UnderlyingType,
			Status: s.Status, LaunchTS: msTime(int64(s.OnboardDate)),
			CtVal: f64ptr(1.0), // D-311 S2：USDT-M qty=基础币数，无合约面值概念
		})
	}
	fund := latestFacts(ctx, b.pool, "binance", "funding", "binance_ws_markprice", "15 minutes")
	ivl := latestFacts(ctx, b.pool, "binance", "funding_interval", "", "15 minutes")
	vol := latestFacts(ctx, b.pool, "binance", "vol24h", "binance_ws_ticker", "30 minutes")
	idxP := latestFacts(ctx, b.pool, "binance", "index_price", "binance_ws_markprice", "15 minutes") // D-591 R2
	mid := latestTicksMid(ctx, b.pool, "binance")
	for i := range rows {
		sym := rows[i].Symbol
		if ap, ok := fund[sym]; ok {
			if sec, ok2 := ivl[sym]; ok2 && sec > 0 {
				// 年化% → 单期小数（D-582 同义换算）；interval 秒 → 小时
				rows[i].FundingRate = ap * sec / (365 * 100 * 86400)
				rows[i].IntervalH = int(math.Round(sec / 3600))
			}
		}
		rows[i].Vol24h = vol[sym]
		rows[i].Mark = mid[sym]   // ≈mark（bookTicker mid）
		rows[i].Index = idxP[sym] // D-591 R2：index 同帧落库；缺=0→basisBps nil 诚实缺（既有守卫）
	}
	return rows, nil
}

// latestFacts 批查 venue kind 最新值（src 非空则锚定；funding_interval 落点 src 异构不过滤）。
// D-591 R3：泛化 venue 参数供 okx/gate 读库化共用。
func latestFacts(ctx context.Context, pool *pgxpool.Pool, venue, kind, src, maxAge string) map[string]float64 {
	q := `SELECT DISTINCT ON (symbol) symbol, value FROM facts
		WHERE kind=$1 AND venue=$2 AND ts > now()-$3::interval`
	args := []any{kind, venue, maxAge}
	if src != "" {
		q += ` AND src=$4`
		args = append(args, src)
	}
	q += ` ORDER BY symbol, ts DESC`
	out := map[string]float64{}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		log.Printf("[universe-radar] %s facts %s: %v", venue, kind, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sym string
		var v float64
		if rows.Scan(&sym, &v) == nil {
			out[sym] = v
		}
	}
	return out
}

// latestTicksMid 批查 venue perp 最新 tick 中价（≈mark 近似）。
func latestTicksMid(ctx context.Context, pool *pgxpool.Pool, venue string) map[string]float64 {
	out := map[string]float64{}
	rows, err := pool.Query(ctx, `SELECT DISTINCT ON (symbol) symbol, (bid+ask)/2 FROM ticks
		WHERE venue=$1 AND venue_type='perp' AND ts > now()-interval '120 seconds'
		ORDER BY symbol, ts DESC`, venue)
	if err != nil {
		log.Printf("[universe-radar] %s ticks: %v", venue, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sym string
		var v float64
		if rows.Scan(&sym, &v) == nil {
			out[sym] = v
		}
	}
	return out
}
