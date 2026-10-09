// ws_proxy_cfg.go —— D-145 env 收口：WS/端点类运行期配置由装配层 boot 注入，
// 本包不再直读 env（未注入=零值=原生直连，与旧 env 缺省语义一致）。
package exchange

// ProxyEndpoints D-145：原 env 直读端点键的注入面（cfg.EnvKnobs 供值）。
type ProxyEndpoints struct {
	CexWSBase          string // ARB_CEX_WS_BASE 主基址（空=直连）
	CexWSBaseBackup    string // ARB_CEX_WS_BASE_BACKUP
	BinanceProxyBackup string // BINANCE_PROXY_URL_BACKUP（binance-mkt-ws 候选#2）
	PerpPublicWSURL    string // ARB_BINANCE_PERP_PUBLIC_WS_URL 逐流覆盖
	PerpMarketWSURL    string // ARB_BINANCE_PERP_MARKET_WS_URL
	SpotWSURL          string // ARB_BINANCE_SPOT_WS_URL
	GateWSMode         string // ARB_GATE_WS_MODE（"direct"=原生直连）
}

var ep ProxyEndpoints

// SetProxyEndpoints boot 注入端点配置（先于一切 collector 构造调用）。
func SetProxyEndpoints(c ProxyEndpoints) { ep = c }
