// gate_pairs.go —— D-537：gate 合法 pair 白名单（订阅边界过滤源）。
// 病灶：spot.tickers 对 payload 中任一非法 pair 静默拒整批（9-28 探针实证：
// 74 对含 COPPER/牛来 等→仅回显帧零数据），futures 容忍同批垃圾但同享过滤绝后患。
// 集源=官方 REST 目录（venue 权威存在性表，同 okxInstCatalog 模式）：
// spot=/api/v4/spot/currency_pairs[].id，futures=/api/v4/futures/usdt/contracts[].name。
// 失败回退=保留上次成功集；从未成功=不过滤（回退现状不放大故障面）。
package exchange

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// gateWLRefresh 白名单刷新周期（挂牌/下架低频；失败保留旧集不闪断）。
const gateWLRefresh = time.Hour

// gatePairWhitelist 单 channel（spot 或 futures）合法 pair 集——atomic.Value
// 存 map[string]struct{}（整集换载读写免锁）；未加载（nil 值）=不过滤。
type gatePairWhitelist struct {
	client *http.Client // cfg.Client（httpx 注入，CA+token 单源）
	url    string       // {GateBaseURL}/api/v4/...（端点单源走配置）
	field  string       // spot="id"；futures="name"
	set    atomic.Value // map[string]struct{}
}

// newGatePairWhitelists 按 cfg.GateBaseURL 装配 spot/futures 两 channel 白名单。
func newGatePairWhitelists(cfg Config) (spot, futures *gatePairWhitelist) {
	c := cfg.Client
	if c == nil { // 裸构造允许空 Client——生产 DefaultConfig 恒带 10s client
		c = http.DefaultClient
	}
	base := cfg.GateBaseURL
	return &gatePairWhitelist{client: c, url: base + "/api/v4/spot/currency_pairs", field: "id"},
		&gatePairWhitelist{client: c, url: base + "/api/v4/futures/usdt/contracts", field: "name"}
}

// refresh 同步拉取目录并整集换载；失败保留上次成功集（≤10s 上限=client 超时兜底）。
func (w *gatePairWhitelist) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var arr []map[string]any
	if err := getJSONInto(ctx, w.client, w.url, &arr); err != nil {
		log.Printf("[gate-ws] pair 白名单拉取失败: %v（沿用旧集；未加载则不过滤）", err)
		return
	}
	set := make(map[string]struct{}, len(arr))
	for _, m := range arr {
		if s, ok := m[w.field].(string); ok && s != "" {
			set[s] = struct{}{}
		}
	}
	w.set.Store(set)
}

// refreshGateWhitelists 周期刷新（Run 后台 goroutine；首拉由 Run 同步完成）。
func refreshGateWhitelists(ctx context.Context, wls ...*gatePairWhitelist) {
	t := time.NewTicker(gateWLRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, w := range wls {
				w.refresh(ctx)
			}
		}
	}
}

// filter pairs∩白名单（保持原序）；白名单未加载（从未成功拉取）=原样透传。
// nil receiver 安全（测试裸构造 GateWebSocket 无白名单=不过滤）。
func (w *gatePairWhitelist) filter(pairs []string) []string {
	if w == nil {
		return pairs
	}
	v := w.set.Load()
	if v == nil {
		return pairs
	}
	set := v.(map[string]struct{})
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if _, ok := set[p]; ok {
			out = append(out, p)
		}
	}
	return out
}
