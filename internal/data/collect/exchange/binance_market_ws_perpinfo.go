// binance_market_ws_perpinfo.go —— indiv perp 存在性闸（normalize 残件②：
// normalize 形对≠venue 存在——SG 实证 xpdusdusdt/clusdtusdt tradifi 名
// normalize 后仍非 binance 对=零帧死连重连搅；spot 侧 spotOK 有目录闸
// perp 侧零闸补）。fapi exchangeInfo 拉一次缓存 TRADING 全符号集（交割
// /tradifi/永续 contractType 全收——存在性判非合约类过滤）；败=fail-open
// 全量订阅+一行 warn（spotOK 同族语义——目录缺期不升级断供）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"nox/internal/platform/httpx"
)

// indivQuarantined 目录不载符号计数透出（拒因计数器族消费面——读侧=告警/
// 诊断面查 indiv 幽灵订阅量；rescan 写、读侧原子）。
var indivQuarantined atomic.Int64

// ensurePerpInfo perp exchangeInfo 拉一次缓存 TRADING 符号集（spotOK 同族——
// 拉败=fail-open+warn，目录缺期不阻塞采集）。复用 infoOnce 闸面不动。
func (b *BinanceMarketWS) ensurePerpInfo(ctx context.Context) {
	b.perpOnce.Do(func() {
		// 走 BinanceApiSegBase 段基址（代理域=裸host+/fapi 前缀路由，原生域=
		// fapi.binance.com/fapi）——裸拼 BaseURL+"/v1/..." 会撞代理 403。
		infoURL := httpx.BinanceApiSegBase(b.cfg.BinanceBaseURL, "fapi") + "/v1/exchangeInfo"
		client := b.cfg.Client // 代理域须 token+CA（spotInfoClient 同款裁定）
		if pu, err := url.Parse(infoURL); err == nil && isNativeBinance(pu.Host) {
			client = b.restNative // 原生域裸 client——token 不泄币安域
		}
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		set, err := fetchPerpExchangeInfo(ctx, client, infoURL)
		if err != nil {
			log.Printf("[binance-mkt-ws] perp exchangeInfo 拉取失败 %v（fail-open 全量订阅）", err)
			return
		}
		b.validPerp = set
	})
}

// perpOK perp 目录存在性判定（validPerp 未建立=fail-open 放行——spotOK 同族 caveat）。
func (b *BinanceMarketWS) perpOK(streamSym string) bool {
	if b.validPerp == nil {
		return true
	}
	_, ok := b.validPerp[strings.ToUpper(streamSym)]
	return ok
}

// fetchPerpExchangeInfo fapi exchangeInfo→TRADING 符号集（全 contractType——
// 存在性判非合约类过滤，tradifi/交割腿同域在集）。
func fetchPerpExchangeInfo(ctx context.Context, client *http.Client, url string) (map[string]struct{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
