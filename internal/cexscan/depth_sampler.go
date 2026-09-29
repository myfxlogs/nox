// depth_sampler.go —— D-578：三所 spot 盘口深度定时采样（资金迁移决策观察窗数据面）。
// 公开端点零签名，每 6h 一拍对齐北京 04/10/16/22 + 启动即补一拍；每 venue×symbol×时点
// 落三行 facts（spread_bps / ±0.5% bid_usd / ask_usd，src=cex_depth_sampler）。
// 单通道失败=该对零行+warn（诚实空态，不造零值）；ARB_DEPTH_SAMPLER_OFF=1 整体停采。
package cexscan

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/httpx"
	"arb/internal/store/pgstore"
)

const (
	depthSrc        = "cex_depth_sampler"
	depthSpreadKind = "spot_depth_spread_bps"
	depthBidKind    = "spot_depth_bid_usd"
	depthAskKind    = "spot_depth_ask_usd"
	depthBand       = 0.005 // ±0.5% 名义深度带
)

var (
	// 测试可覆写端点基址（生产=常量公开域）。
	binanceDepthBase = "https://data-api.binance.vision" // D-551 免墙公开域先例
	gateDepthBase    = "https://api.gateio.ws"
	// bjDepth 北京时区（FixedZone 免 tzdata，D-510 先例）。
	bjDepth = time.FixedZone("Asia/Shanghai", 8*3600)
)

// depthVenueSyms canonical → 三所原生 symbol（D-578 起步集）。
var depthVenueSyms = map[string]map[string]string{
	"XAUT": {"binance": "XAUTUSDT", "gate": "XAUT_USDT", "okx": "XAUT-USDT"},
	"PAXG": {"binance": "PAXGUSDT", "gate": "PAXG_USDT", "okx": "PAXG-USDT"},
}

// depthSampler 三所深度采样器（挂 Scanner.Run 同生命周期）。
type depthSampler struct {
	client  *http.Client
	store   *pgstore.Store
	okxBase string // okx spot base_url（cex-proxy，含 /api 前缀与否皆可——端点路径自带）
	now     func() time.Time
}

// newDepthSampler 装配（okxBase=exchange_configs 的 okx spot base_url，空=okx 通道跳过+warn）。
func newDepthSampler(store *pgstore.Store, okxBase string) *depthSampler {
	if okxBase == "" {
		okxBase = os.Getenv("ARB_OKX_PROXY_URL")
	}
	return &depthSampler{client: httpx.Client(), store: store, okxBase: okxBase, now: time.Now}
}

// depthSymbols 采样集（env ARB_DEPTH_SYMBOLS 逗号分隔 canonical 可扩）。
func depthSymbols() []string {
	raw := os.Getenv("ARB_DEPTH_SYMBOLS")
	if raw == "" {
		out := make([]string, 0, len(depthVenueSyms))
		for c := range depthVenueSyms {
			out = append(out, c)
		}
		sort.Strings(out)
		return out
	}
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// nextDepthTick 下一拍（北京 04/10/16/22 对齐）。
func nextDepthTick(now time.Time) time.Time {
	bj := now.In(bjDepth)
	for _, h := range []int{4, 10, 16, 22} {
		c := time.Date(bj.Year(), bj.Month(), bj.Day(), h, 0, 0, 0, bjDepth)
		if c.After(bj) {
			return c
		}
	}
	return time.Date(bj.Year(), bj.Month(), bj.Day()+1, 4, 0, 0, 0, bjDepth)
}

// Run 常驻采样循环：启动补一拍 → 对齐拍。阻塞至 ctx 取消或 env 关闭。
func (d *depthSampler) Run(ctx context.Context) {
	if os.Getenv("ARB_DEPTH_SAMPLER_OFF") == "1" {
		log.Printf("[depth_sampler] ARB_DEPTH_SAMPLER_OFF=1，停采")
		return
	}
	if err := d.sampleOnce(ctx); err != nil {
		log.Printf("[depth_sampler] initial tick: %v", err)
	}
	lastDay := d.now().In(bjDepth).Day()
	for {
		next := nextDepthTick(d.now())
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if err := d.sampleOnce(ctx); err != nil {
			log.Printf("[depth_sampler] tick: %v", err)
		}
		if day := d.now().In(bjDepth).Day(); day != lastDay {
			log.Printf("[depth_sampler] alive, daily first tick done")
			lastDay = day
		}
	}
}

// sampleOnce 一拍：三通道×品种逐对拉盘口→三行 facts。单对失败=零行+warn，不阻断其余。
func (d *depthSampler) sampleOnce(ctx context.Context) error {
	var facts []fact.Fact
	for _, canon := range depthSymbols() {
		for _, venue := range []string{"binance", "gate", "okx"} {
			native := depthVenueSyms[canon][venue]
			if native == "" {
				continue // env 扩了未映射 canonical → 跳过该 venue
			}
			bids, asks, err := d.fetchBook(ctx, venue, native)
			if err != nil {
				log.Printf("[depth_sampler] %s %s: %v（本对零行，诚实空态）", venue, canon, err)
				continue
			}
			facts = append(facts, depthFacts(venue, canon, bids, asks, d.now())...)
		}
	}
	if len(facts) == 0 {
		return fmt.Errorf("all channels empty")
	}
	return d.store.InsertFacts(ctx, facts)
}

// depthFacts 盘口→三行 facts（纯函数可测）：top-of-book 点差 bps + ±0.5% 带内名义深度。
func depthFacts(venue, canon string, bids, asks [][2]float64, ts time.Time) []fact.Fact {
	if len(bids) == 0 || len(asks) == 0 {
		return nil
	}
	mid := (bids[0][0] + asks[0][0]) / 2
	if mid <= 0 {
		return nil
	}
	spread := (asks[0][0] - bids[0][0]) / mid * 1e4
	var bidUSD, askUSD float64
	for _, l := range bids {
		if l[0] >= mid*(1-depthBand) {
			bidUSD += l[0] * l[1]
		}
	}
	for _, l := range asks {
		if l[0] <= mid*(1+depthBand) {
			askUSD += l[0] * l[1]
		}
	}
	vk := venue + ":spot"
	mk := func(kind, unit string, v float64) fact.Fact {
		return fact.Fact{Kind: kind, Venue: vk, Symbol: canon, Value: v, Unit: unit, Ts: ts, Src: depthSrc}
	}
	return []fact.Fact{
		mk(depthSpreadKind, "bps", spread),
		mk(depthBidKind, "usd", bidUSD),
		mk(depthAskKind, "usd", askUSD),
	}
}

// fetchBook 三通道盘口拉取（公开端点零签名；venue-native symbol）。
// 三所 bids/asks 均为 ["price","qty"] 字符串对——统一解析防 JSON 数字/串双形。
func (d *depthSampler) fetchBook(ctx context.Context, venue, native string) (bids, asks [][2]float64, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rawBids, rawAsks [][2]string
	switch venue {
	case "binance":
		var r struct {
			Bids [][2]string `json:"bids"`
			Asks [][2]string `json:"asks"`
		}
		err = d.getJSON(ctx, fmt.Sprintf("%s/api/v3/depth?symbol=%s&limit=100", binanceDepthBase, native), &r)
		rawBids, rawAsks = r.Bids, r.Asks
	case "gate":
		var r struct {
			Bids [][2]string `json:"bids"`
			Asks [][2]string `json:"asks"`
		}
		err = d.getJSON(ctx, fmt.Sprintf("%s/api/v4/spot/order_book?currency_pair=%s&limit=100", gateDepthBase, native), &r)
		rawBids, rawAsks = r.Bids, r.Asks
	case "okx":
		if d.okxBase == "" {
			return nil, nil, fmt.Errorf("okx base_url 未配置（cex-proxy env/exchange_config 均空）")
		}
		var r struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
			Data []struct {
				Bids [][2]string `json:"bids"`
				Asks [][2]string `json:"asks"`
			} `json:"data"`
		}
		if err = d.getJSON(ctx, fmt.Sprintf("%s/api/v5/market/books?instId=%s&sz=100", strings.TrimRight(d.okxBase, "/"), native), &r); err != nil {
			return nil, nil, err
		}
		if r.Code != "0" || len(r.Data) == 0 {
			return nil, nil, fmt.Errorf("okx books code=%s msg=%s", r.Code, r.Msg)
		}
		rawBids, rawAsks = r.Data[0].Bids, r.Data[0].Asks
	default:
		return nil, nil, fmt.Errorf("unknown venue %q", venue)
	}
	if err != nil {
		return nil, nil, err
	}
	bids, err = parseLevels(rawBids)
	if err != nil {
		return nil, nil, fmt.Errorf("bids parse: %w", err)
	}
	asks, err = parseLevels(rawAsks)
	if err != nil {
		return nil, nil, fmt.Errorf("asks parse: %w", err)
	}
	return bids, asks, nil
}

func parseLevels(raw [][2]string) ([][2]float64, error) {
	out := make([][2]float64, 0, len(raw))
	for _, l := range raw {
		p, err := strconv.ParseFloat(l[0], 64)
		if err != nil {
			return nil, err
		}
		q, err := strconv.ParseFloat(l[1], 64)
		if err != nil {
			return nil, err
		}
		out = append(out, [2]float64{p, q})
	}
	return out, nil
}

func (d *depthSampler) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
