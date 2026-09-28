// binance_market_ws_spotinfo.go —— D-551 S2：现货 exchangeInfo 有效性过滤。
// 拉一次缓存 TRADING 符号集，spot 订阅参数先过滤再出（无效流名白占上游突发
// 宽度）；拉取失败=fail-open 全量订阅+一行 warn，不阻塞采集。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// ensureSpotInfo D-551 S2：现货 exchangeInfo 拉一次缓存有效符号集——订阅参数先
// 过滤再出（无效流名白占上游突发宽度）。拉取失败=fail-open 全量订阅+一行 warn。
func (b *BinanceMarketWS) ensureSpotInfo(ctx context.Context) {
	b.infoOnce.Do(func() {
		fetch := b.spotInfoFetch
		if fetch == nil {
			client := b.spotInfoClient
			if client == nil {
				client = http.DefaultClient
			}
			fetch = func(ctx context.Context, u string) (map[string]struct{}, error) {
				return fetchSpotExchangeInfo(ctx, client, u)
			}
		}
		set, err := fetch(ctx, b.spotInfoURL)
		if err != nil {
			log.Printf("[binance-mkt-ws] spot exchangeInfo 拉取失败 %v（fail-open 全量订阅）", err)
			return
		}
		b.validSpot = set
	})
}

// spotOK D-551 S2：spot 订阅有效性判定（validSpot 未建立=fail-open 放行）。
func (b *BinanceMarketWS) spotOK(sym string) bool {
	if b.validSpot == nil {
		return true
	}
	_, ok := b.validSpot[strings.ToUpper(sym)+"USDT"]
	return ok
}

// fetchSpotExchangeInfo 现货 exchangeInfo→TRADING 符号集（symbol 键大写原名）。
func fetchSpotExchangeInfo(ctx context.Context, client *http.Client, infoURL string) (map[string]struct{}, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", infoURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	var info struct {
		Symbols []struct {
			Symbol string `json:"symbol"`
			Status string `json:"status"`
		} `json:"symbols"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(info.Symbols))
	for _, s := range info.Symbols {
		if s.Status == "TRADING" {
			set[s.Symbol] = struct{}{}
		}
	}
	return set, nil
}

// bookTickerFrame 双形制统一投影（perp 带 e/T/E 包裹、spot 裸 u/s/b/B/a/A——取交集字段）。
// bookTickerFrame 手工取键：载荷同时含 b(价)/B(量)、a(价)/A(量)——Go json tag
// 大小写不敏感回退会令 "B" 覆盖 "b" 绑定（实测 bid=2.5=量），禁 struct tag 直解。
type bookTickerFrame struct {
	S  string
	B  string // bid 价（小写 b）
	A  string // ask 价（小写 a）
	BQ string // bid 量（大写 B）
	AQ string // ask 量（大写 A）
	T  int64  // perp 有；spot 零值→本地 now
}

func parseBookTicker(data []byte) (bookTickerFrame, bool) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return bookTickerFrame{}, false
	}
	// D-535：!bookTicker 聚合流帧带 {"stream","data"} 包裹层——下钻到 data；
	// 裸帧（spot/逐符号形制）无 data 键原样透用，双形兼容。
	if d, ok := m["data"].(map[string]any); ok {
		m = d
	}
	f := bookTickerFrame{}
	if v, ok := m["s"].(string); ok {
		f.S = v
	}
	if v, ok := m["b"].(string); ok {
		f.B = v
	}
	if v, ok := m["a"].(string); ok {
		f.A = v
	}
	if v, ok := m["B"].(string); ok {
		f.BQ = v
	}
	if v, ok := m["A"].(string); ok {
		f.AQ = v
	}
	if v, ok := m["T"].(float64); ok {
		f.T = int64(v)
	}
	return f, f.S != "" && f.B != "" && f.A != ""
}
