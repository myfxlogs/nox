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
	e := &Engine{
		cfg:              cfg,
		store:            store,
		service:          service,
		oppTopic:         oppTopic,
		quotes:           quotes,
		gate:             NewGate(store, cfg, cp),
		fundingNeedSent:  make(map[string]string),
		fundingBreach:    make(map[int64]int),  // D-133 T1：PERP_FUNDING 去抖计数
		disasterBreach:   make(map[int64]int),  // D-378b S1：灾难带去抖计数
		csMissCounts:     make(map[string]int), // D-140 S2：CS-miss 聚合
		csMissSent:       make(map[string]time.Time),
		marginAlertSent:  make(map[string]time.Time), // D-144 ②：保证金告警去重
		revivalAlertSent: make(map[string]time.Time), // D-256-A S5：复活扳机告警去重
		kickCh:           make(chan struct{}, 1),     // D-107 P4：buffered(1) 非阻塞 send = 事件合并
	}
	// D-405 S4：开户门槛档——dashboard.MarginFloorCheck 注入（nil service=未接线不检查）。
	if service != nil {
		e.gate.SetMarginFloorChecker(service)
	}
	// D-403 S3：热重载指针初值=启动加载结果（cfg.XAssetAllowCombos，setup 期 xassetAllowFromTable 产出）。
	initAllow := cfg.XAssetAllowCombos
	e.xassetAllow.Store(&initAllow)
	return e
}
