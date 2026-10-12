// cex_positions_querier.go —— CEXPositionQuerier 主体（D-362 自 cex_positions.go 拆出，
// 保 check-lines <300；行为零变更，keyOverride=D-362 S2b 撞键归一键）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"nox/internal/data/keydomain"
	"nox/internal/data/venue"
	"nox/internal/platform/httpx"
)

// CEXPositionQuerier 查询 CEX perp 持仓（实现 venue.Querier）；字段同 CEXAccountQuerier（复用签名逻辑），但查持仓而非余额。
type CEXPositionQuerier struct {
	venue       string // okx / gate / binance
	venueType   string // perp（spot 无持仓查询）
	apiKey      string
	apiSecret   string
	passphrase  string // OKX 必填
	baseURL     string
	simulated   bool
	keyOverride string // D-362 S2b：撞键非赢家注册键（空=canonical）
	client      *http.Client

	// binance openedAt 推导缓存（positionRisk 无开仓时间→userTrades 一次/持仓期）——
	// "symbol|side"→openedAt；持仓离集/换向失效；ts 零值=不可得负缓存（防每轮重打 venue）。
	openedAtMu    sync.Mutex
	openedAtCache map[string]openedAtEntry
}

// NewCEXPositionQuerier 创建 CEX perp 持仓查询器。
func NewCEXPositionQuerier(venue, venueType, apiKey, apiSecret, passphrase, baseURL string, simulated bool) *CEXPositionQuerier {
	return &CEXPositionQuerier{
		venue:      venue,
		venueType:  venueType,
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		passphrase: passphrase,
		baseURL:    baseURL,
		simulated:  simulated,
		client:     httpx.Client(), // CA 信任 + X-Proxy-Token（cex-proxy 自签证书，D-055 同款）
	}
}

// 撞键非赢家经 SetKeyOverride 返回 "venue:type#<id>"——查询结果
// BrokerPosition.VenueKey 随之带后缀（真仓在持仓视图独立现形，SSOT §2b/A8）。
func (q *CEXPositionQuerier) VenueKey() string {
	if q.keyOverride != "" {
		return q.keyOverride
	}
	return keydomain.Join(q.venue, q.venueType)
}

// SetKeyOverride D-362 S2b：设置撞键归一后的注册键（由 reconcile.CexVenueKey 计算）。
func (q *CEXPositionQuerier) SetKeyOverride(k string) { q.keyOverride = k }

// QueryOpenedOrders 查询全部 perp 持仓 → []venue.Order。
// spot venue 返回空（无持仓概念）；perp 按 venue 分派。
func (q *CEXPositionQuerier) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	if q.venueType != "perp" {
		return nil, nil // spot 无持仓查询
	}
	switch q.venue {
	case "okx":
		return q.okxPositions(ctx)
	case "gate":
		return q.gatePositions(ctx)
	case "binance":
		return q.binancePositions(ctx)
	default:
		return nil, fmt.Errorf("cex positions: unsupported venue %q", q.venue)
	}
}
