package autotrade

import (
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/store/pgstore"
)

// New 创建自动交易引擎（从 engine.go 提取避免超 300 行）。
func New(cfg Config, store *pgstore.Store, service *dashboard.Service, oppTopic *bus.Topic[pgstore.Opportunity], quotes *bus.QuoteBus) *Engine {
	cp := NewControlPlane(store)
	cp.SetLiveGateChecker(cfg.LiveGateChecker) // D-195-C1：LIVE 导出门注入（nil 安全）
	return &Engine{
		cfg:              cfg,
		store:            store,
		service:          service,
		oppTopic:         oppTopic,
		quotes:           quotes,
		gate:             NewGate(store, cfg, cp),
		fundingNeedSent:  make(map[string]string),
		fundingBreach:    make(map[int64]int),  // D-133 T1：PERP_FUNDING 去抖计数
		csMissCounts:     make(map[string]int), // D-140 S2：CS-miss 聚合
		csMissSent:       make(map[string]time.Time),
		marginAlertSent:  make(map[string]time.Time), // D-144 ②：保证金告警去重
		revivalAlertSent: make(map[string]time.Time), // D-256-A S5：复活扳机告警去重
		kickCh:           make(chan struct{}, 1),     // D-107 P4：buffered(1) 非阻塞 send = 事件合并
	}
}
