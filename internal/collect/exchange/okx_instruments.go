// okx_instruments.go —— D-524：OKX instruments 目录闸（watchlist 方言归一化 +
// instId 存在性校验）。radar_watchlist symbol 列五方言混杂（bare=BTC /
// canonical=BTCUSDT / gate=BTC_USDT / okx spot=BTC-USDT / okx perp=BTC-USDT-SWAP），
// 旧码统一裸拼 -USDT/-USDT-SWAP 产出不存在 instId，被服务端 60018 拒订刷屏
// （实证：XTZUSDT-USDT、牛来-USDT、BZ-USDT-SWAP-USDT-SWAP 等大批 error 帧）。
// 目录=venue 权威返回值（public instruments 端点），不在目录的符号跳过不伪造。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// okxCatalogTTL instruments 目录刷新周期（挂牌/下架低频；失败保留旧集不闪断）。
const okxCatalogTTL = 30 * time.Minute

// okxSubCandidates 任意方言符号 → okx USDT 本位候选 instId（spot+perp）。
// 返回值只是候选——是否真实存在由 okxInstCatalog 裁决（fail-closed：不伪造）。
func okxSubCandidates(sym string) (spotID, perpID string) {
	s := strings.ToUpper(strings.TrimSpace(sym))
	base := s
	switch {
	case strings.HasSuffix(s, "-USDT-SWAP"):
		base = strings.TrimSuffix(s, "-USDT-SWAP")
	case strings.HasSuffix(s, "-USDT"):
		base = strings.TrimSuffix(s, "-USDT")
	case strings.HasSuffix(s, "_USDT"):
		base = strings.TrimSuffix(s, "_USDT")
	case strings.HasSuffix(s, "USDT"):
		base = strings.TrimSuffix(s, "USDT")
	}
	if base == "" {
		return "", ""
	}
	return base + "-USDT", base + "-USDT-SWAP"
}

// okxInstCatalog okx 存活 instruments 目录（SPOT ∪ SWAP，venue 权威存在性表）。
// 源=GET {OKXBaseURL}/api/v5/public/instruments?instType=X（公开端点，经 cex-proxy
// 时 cfg.Client 自动注入 token）。ensure 过期再拉；拉取失败保留上次成功集。
type okxInstCatalog struct {
	client *http.Client
	base   string

	mu     sync.Mutex
	set    map[string]bool // state=live 的 instId
	at     time.Time       // 上次成功时刻
	failed bool            // 上次拉取失败标（失败日志只打一次）
}

func newOKXInstCatalog(cfg Config) *okxInstCatalog {
	return &okxInstCatalog{client: cfg.Client, base: strings.TrimRight(cfg.OKXBaseURL, "/")}
}

// ensure 目录未载/过期则同步拉取（TTL 内零开销；调用方一次 REST 往返上限=cfg.Client 超时）。
func (c *okxInstCatalog) ensure(ctx context.Context) {
	c.mu.Lock()
	fresh := c.set != nil && time.Since(c.at) < okxCatalogTTL
	c.mu.Unlock()
	if fresh {
		return
	}
	if err := c.fetch(ctx); err != nil {
		c.mu.Lock()
		first := !c.failed
		c.failed = true
		c.mu.Unlock()
		if first {
			log.Printf("[okx-ws] instruments 目录拉取失败: %v（沿用旧集；未加载则回退未过滤订阅）", err)
		}
	}
}

func (c *okxInstCatalog) fetch(ctx context.Context) error {
	set := make(map[string]bool, 2048)
	for _, instType := range []string{"SPOT", "SWAP"} {
		var out struct {
			Code string `json:"code"`
			Data []struct {
				InstID string `json:"instId"`
				State  string `json:"state"`
			} `json:"data"`
		}
		req, err := http.NewRequestWithContext(ctx, "GET",
			fmt.Sprintf("%s/api/v5/public/instruments?instType=%s", c.base, instType), nil)
		if err != nil {
			return err
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return fmt.Errorf("instruments %s: %w", instType, err)
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("instruments %s decode: %w", instType, err)
		}
		if out.Code != "0" {
			return fmt.Errorf("instruments %s: code=%s", instType, out.Code)
		}
		for _, i := range out.Data {
			if i.State == "live" {
				set[i.InstID] = true
			}
		}
	}
	c.mu.Lock()
	c.set, c.at, c.failed = set, time.Now(), false
	c.mu.Unlock()
	return nil
}

// loaded 目录是否已成功拉取过（false → pick 透传候选，判决不可用）。
func (c *okxInstCatalog) loaded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.set != nil
}

// pick 候选∩目录 → 存活 instId（目录未加载=透传候选——可用性优先，非法 id
// 由服务端 error 帧留痕兜底）。
func (c *okxInstCatalog) pick(spotID, perpID string) (spot, perp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set == nil {
		return spotID, perpID
	}
	if c.set[spotID] {
		spot = spotID
	}
	if c.set[perpID] {
		perp = perpID
	}
	return
}
