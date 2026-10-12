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

// ---------- OKX（D-591 R3：instruments/tickers SWAP → 24h 磁盘缓存 + funding → facts 读库） ----------
// 目录/聚合 ticker 低频数据 24h 缓存降频（binance exchangeInfo 同款形制：新鲜直用/拉败回落过期）；
// funding=okx_ws_v5 WS 覆盖集 facts，未覆盖符号 FundingRate/IntervalH 零值诚实跳过。
type okx struct {
	hc       *http.Client
	base     string
	pool     *pgxpool.Pool
	cacheDir string
	throttle time.Duration // funding-rate 逐 instId 限频间隔（测试置 0）
}

func (o okx) Venue() string { return "okx" }

// okxSwapCache instruments+tickers 双载荷 24h 快照。
type okxSwapCache struct {
	FetchedAt   time.Time       `json:"fetched_at"`
	Instruments []okxInstrument `json:"instruments"`
	Tickers     []okxTicker     `json:"tickers"`
}

type okxInstrument struct {
	InstID   string    `json:"instId"`
	State    string    `json:"state"`
	ListTime flexInt   `json:"listTime"` // ms
	CtVal    flexFloat `json:"ctVal"`    // 合约面值（base 单位，BTC-USDT-SWAP=0.01）
}

type okxTicker struct {
	InstID    string    `json:"instId"`
	VolCcy24h flexFloat `json:"volCcy24h"`
	Last      flexFloat `json:"last"`
}

// swapCached 24h 缓存：新鲜直用；过期/缺失拉双端点并落盘；拉失败有过期缓存→回落。
func (o okx) swapCached(ctx context.Context) (okxSwapCache, error) {
	var out okxSwapCache
	path := filepath.Join(o.cacheDir, "okx-swap.json")
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &out) == nil {
		if time.Since(out.FetchedAt) < 24*time.Hour {
			return out, nil
		}
	}
	fresh, err := o.swapFetch(ctx)
	if err != nil {
		if out.FetchedAt.IsZero() {
			return out, fmt.Errorf("okx swap snapshot: %w", err)
		}
		log.Printf("[universe-radar] okx instruments/tickers 刷新失败，回落过期缓存（%s）: %v",
			out.FetchedAt.Format(time.RFC3339), err)
		return out, nil
	}
	out = fresh
	if raw, err := json.Marshal(out); err == nil {
		_ = os.MkdirAll(o.cacheDir, 0o755)
		_ = os.WriteFile(path, raw, 0o644)
	}
	return out, nil
}

func (o okx) swapFetch(ctx context.Context) (okxSwapCache, error) {
	var ins struct {
		Data []okxInstrument `json:"data"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/v5/public/instruments?instType=SWAP", &ins); err != nil {
		return okxSwapCache{}, fmt.Errorf("instruments: %w", err)
	}
	var tk struct {
		Data []okxTicker `json:"data"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/v5/market/tickers?instType=SWAP", &tk); err != nil {
		return okxSwapCache{}, fmt.Errorf("market/tickers: %w", err)
	}
	return okxSwapCache{FetchedAt: time.Now(), Instruments: ins.Data, Tickers: tk.Data}, nil
}

func (o okx) Scan(ctx context.Context) ([]Row, error) {
	snap, err := o.swapCached(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(snap.Instruments))
	idx := make(map[string]int)
	for _, d := range snap.Instruments {
		if !strings.HasSuffix(d.InstID, "-USDT-SWAP") {
			continue
		}
		st := d.State
		if st == "live" {
			st = "TRADING"
		}
		idx[d.InstID] = len(rows)
		rows = append(rows, Row{
			Symbol: d.InstID, Base: strings.TrimSuffix(d.InstID, "-USDT-SWAP"),
			Status: st, LaunchTS: msTime(int64(d.ListTime)),
			CtVal: f64ptr(float64(d.CtVal)),
		})
	}
	// D-205②：funding 换 venue 公共 REST——/api/v5/public/funding-rate 逐 instId
	// 循环限频（不依赖订阅集，全量 SWAP 可评）；interval=(nextFundingTime-fundingTime)
	// 同帧真值。per-instId 读败→跳过（行在值空如实报）；facts 仅兜底填缺口。
	instBySym := make(map[string]string, len(idx)) // facts 键形 BTCUSDT → instId BTC-USDT-SWAP
	for instID := range idx {
		instBySym[strings.ReplaceAll(strings.TrimSuffix(instID, "-SWAP"), "-", "")] = instID
	}
	funded := 0
	fund := latestFacts(ctx, o.pool, "okx", "funding", []string{"okx_ws_v5"}, "15 minutes")
	ivl := latestFacts(ctx, o.pool, "okx", "funding_interval", []string{"okx_ws_v5"}, "15 minutes")
	for instID := range idx {
		i := idx[instID]
		if fr, err := o.fundingRate(ctx, instID); err == nil {
			rows[i].FundingRate = fr.rate
			rows[i].IntervalH = fr.ivlH
			funded++
			continue
		}
		sym := strings.ReplaceAll(strings.TrimSuffix(instID, "-SWAP"), "-", "")
		if ap, ok := fund[sym]; ok { // facts 兜底（年化%→单期小数，同 binance 式）
			if sec, ok2 := ivl[sym]; ok2 && sec > 0 {
				rows[i].FundingRate = ap * sec / (365 * 100 * 86400)
				rows[i].IntervalH = int(math.Round(sec / 3600))
				funded++
			}
		}
	}
	fundNote("okx", len(rows), funded)
	for _, d := range snap.Tickers {
		if i, ok := idx[d.InstID]; ok {
			rows[i].Vol24h = float64(d.VolCcy24h) * float64(d.Last)
			rows[i].Mark = float64(d.Last)
		}
	}
	return rows, nil
}

// okxFunding D-205②：/api/v5/public/funding-rate 单 instId 读——
// rate=fundingRate（当期单期小数）、ivlH=(nextFundingTime−fundingTime)/1h 同帧真值。
type okxFunding struct {
	rate float64
	ivlH int
}

// fundingRate 逐 instId 读+限频（throttle 间隔；venue 官方公共端点无凭据）。
func (o okx) fundingRate(ctx context.Context, instID string) (okxFunding, error) {
	if o.throttle > 0 {
		select {
		case <-ctx.Done():
			return okxFunding{}, ctx.Err()
		case <-time.After(o.throttle):
		}
	}
	var r struct {
		Data []struct {
			FundingRate     flexFloat `json:"fundingRate"`
			FundingTime     flexInt   `json:"fundingTime"`
			NextFundingTime flexInt   `json:"nextFundingTime"`
		} `json:"data"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/v5/public/funding-rate?instId="+instID, &r); err != nil {
		return okxFunding{}, err
	}
	if len(r.Data) == 0 {
		return okxFunding{}, fmt.Errorf("funding-rate %s 无行", instID)
	}
	d := r.Data[0]
	ivlH := 0
	if d.NextFundingTime > 0 && d.FundingTime > 0 {
		ivlH = int(math.Round(float64(d.NextFundingTime-d.FundingTime) / 3600000))
	}
	return okxFunding{rate: float64(d.FundingRate), ivlH: ivlH}, nil
}
