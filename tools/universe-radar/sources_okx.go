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
	InstType string    `json:"instType"` // SWAP/FUTURES（载荷合并后按此分拣）
	State    string    `json:"state"`
	ListTime flexInt   `json:"listTime"` // ms
	CtVal    flexFloat `json:"ctVal"`    // 合约面值（base 单位，BTC-USDT-SWAP=0.01）
	ExpTime  flexInt   `json:"expTime"`  // ms；FUTURES 交割日
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
	var ins []okxInstrument
	var tk []okxTicker
	for _, it := range []string{"SWAP", "FUTURES"} { // 交割轴扩列（OPTION 不收——Greeks/premium 非基差轴）
		var insResp struct {
			Data []okxInstrument `json:"data"`
		}
		if err := getJSON(ctx, o.hc, o.base+"/api/v5/public/instruments?instType="+it, &insResp); err != nil {
			return okxSwapCache{}, fmt.Errorf("instruments %s: %w", it, err)
		}
		ins = append(ins, insResp.Data...)
		var tkResp struct {
			Data []okxTicker `json:"data"`
		}
		if err := getJSON(ctx, o.hc, o.base+"/api/v5/market/tickers?instType="+it, &tkResp); err != nil {
			return okxSwapCache{}, fmt.Errorf("market/tickers %s: %w", it, err)
		}
		tk = append(tk, tkResp.Data...)
	}
	return okxSwapCache{FetchedAt: time.Now(), Instruments: ins, Tickers: tk}, nil
}

func (o okx) Scan(ctx context.Context) ([]Row, error) {
	snap, err := o.swapCached(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(snap.Instruments))
	idx := make(map[string]int)
	for _, d := range snap.Instruments {
		r := Row{Symbol: d.InstID, LaunchTS: msTime(int64(d.ListTime)), CtVal: f64ptr(float64(d.CtVal))}
		switch {
		case strings.HasSuffix(d.InstID, "-USDT-SWAP"):
			r.ContractType = ctPerp
			r.Base = strings.TrimSuffix(d.InstID, "-USDT-SWAP")
		case d.InstType == "FUTURES" && len(strings.Split(d.InstID, "-")) == 3 &&
			okxLinearMid[strings.Split(d.InstID, "-")[1]]: // BTC-USDT-241227 / BTC-USD_UM-250321 形
			r.ContractType = ctFutures
			r.Base = strings.Split(d.InstID, "-")[0]
			r.ExpiryTS = msTime(int64(d.ExpTime)) // 0/缺→零值→basis_ann nil fail-closed
		default:
			continue
		}
		st := d.State
		if st == "live" {
			st = "TRADING"
		}
		r.Status = st
		idx[d.InstID] = len(rows)
		rows = append(rows, r)
	}
	// D-205②：funding 换 venue 公共 REST——/api/v5/public/funding-rate 逐 instId
	// 循环限频（不依赖订阅集，全量 SWAP 可评）；interval=(nextFundingTime-fundingTime)
	// 同帧真值。per-instId 读败→跳过（行在值空如实报）；facts 仅兜底填缺口。
	funded := 0
	fund := latestFacts(ctx, o.pool, "okx", "funding", []string{"okx_ws_v5"}, "15 minutes")
	ivl := latestFacts(ctx, o.pool, "okx", "funding_interval", []string{"okx_ws_v5"}, "15 minutes")
	for instID := range idx {
		i := idx[instID]
		if rows[i].ContractType == ctFutures {
			continue // 交割无 funding 轴——不打 funding-rate 端点（venue 侧本就无该数据）
		}
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
	// FUTURES basis 轴 index：标的现货指数 /index-tickers?uly=BASE-USDT 逐 uly 取
	//（交割 uly 数屈指低频；读败→Index 留 0→basis_ann nil 诚实缺，禁猜）。
	ulys := make(map[string]bool)
	rowUly := make(map[string]string)
	for i := range rows {
		if rows[i].ContractType == ctFutures && rows[i].Base != "" {
			uly := okxIndexUly(rows[i].Symbol)
			if uly != "" {
				ulys[uly] = true
				rowUly[rows[i].Symbol] = uly
			}
		}
	}
	for uly := range ulys {
		if px, err := o.indexPx(ctx, uly); err == nil {
			for i := range rows {
				if rowUly[rows[i].Symbol] == uly {
					rows[i].Index = px
				}
			}
		} else {
			log.Printf("[universe-radar] okx index-tickers %s 读败（basis_ann 该行 NULL）: %v", uly, err)
		}
	}
	return rows, nil
}

// okxLinearMid FUTURES 中段白名单：USDT/USDC 稳定币本位 + USD_UM（USDⓈ篮结算线性交割，
// SG 实证 OKX 稳定币交割腿实面=USD_UM）。USD 中段=币本位逆向合约（settleCcy=币）不收。
var okxLinearMid = map[string]bool{"USDT": true, "USDC": true, "USD_UM": true}

// okxIndexUly FUTURES instId→index-tickers 的 uly：USD_UM→BASE-USD（USDⓈ 篮基差对
// USD 指数），其余→BASE-mid。非三段/中段非白名单→""（不调 index 端点）。
func okxIndexUly(instID string) string {
	p := strings.Split(instID, "-")
	if len(p) != 3 || !okxLinearMid[p[1]] {
		return ""
	}
	mid := p[1]
	if mid == "USD_UM" {
		mid = "USD"
	}
	return p[0] + "-" + mid
}

// indexPx /api/v5/market/index-tickers?uly=<BASE-USDT>→idxPx（venue 官方公共端点无凭据）。
func (o okx) indexPx(ctx context.Context, uly string) (float64, error) {
	var r struct {
		Data []struct {
			IdxPx flexFloat `json:"idxPx"`
		} `json:"data"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/v5/market/index-tickers?uly="+uly, &r); err != nil {
		return 0, err
	}
	if len(r.Data) == 0 {
		return 0, fmt.Errorf("index-tickers %s 无行", uly)
	}
	return float64(r.Data[0].IdxPx), nil
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
