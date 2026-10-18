// main_monitors_args.go —— D-508 S9 行数折分（自 main_monitors.go 搬出；monitorsSetupArgs 纯搬家零改动）。
package main

import (
	"context"
	"nox/internal/data/collect/cmc"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/collect/exchange"
	"nox/internal/ir/notify"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/alertcool"
	"nox/internal/platform/config"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/engine"
	"nox/internal/trading/exec"
)

// monitorsSetupArgs 封装 setupMonitorsAndServices 入参（避免长参数列表）。
type monitorsSetupArgs struct {
	ctx          context.Context
	cancel       context.CancelFunc
	cfg          *config.Config
	acDefaults   *config.Config         // D-523：纯默认快照（键视图 defaultValue 源）
	acEnvSnap    map[string]string      // D-523：overlay 前 env 快照（键视图 envValue 源）
	secRes       *config.SecretResolver // D-545：密钥热读缓存（dashSvc.SetSecretResolver 注入）
	st           *pgstore.Store
	qbus         *bus.QuoteBus
	execReg      *exec.Registry
	liveGate     *gate.LiveGateChecker // D-620⑦：真金单闸检查器单实例（main 早建下穿）
	cp           *gate.ControlPlane    // D-620⑦：控制面单实例（dashboard/dciexec/selfwatch 共注）
	brokerMgr    *dashboard.BrokerManager
	pipeline     *exec.Pipeline
	recon        *reconcile.Reconciler
	factCache    *engine.FactCache
	cmcWs        *cmc.WsClient // D-212：dashSvc.SetCmcLiveProvider/SetCmcWsHealth 注入源（nil=REST 兜底径）
	oppTopic     *bus.Topic[pgstore.Opportunity]
	orderTopic   *bus.Topic[bus.OrderEvent]
	alertTopic   *bus.Topic[pgstore.Alert]
	hedgeTopic   *bus.Topic[struct{}] // D-231 S5：对冲结构刷新通知总线
	refreshTopic *bus.Topic[string]   // D-508 S9：域刷新通知总线（→StreamUpdates case 14）
	cexCfg       exchange.Config      // D-337R3 S1：CEX 采集配置（funding 结算周期缓存 Populate 用）
	// gateWS D-570 包C S6⑤：公开 WS 实例 ref（OnContractInfo 装配寻址——cexStreamMgr
	// 后造，startQuoteStreams 构造期闭包对象不在场）。
	gateWS       *exchange.GateWebSocket
	reviewRun    func()
	commResolver func(venueKey string) float64
	dsn          string
	kickFn       func(string)         // D-249 S3：scheduler kick 注入（dashboard.TrxVoteAprRefresh on-open 触发采集）
	feishuClient *notify.FeishuClient // D-270 S3：webhook 回复解析需持有 FeishuClient
	alertCooler  *alertcool.Cooler    // D-301c S2：告警同指纹冷却单实例（main 域创建透传，cexAlertFn 收口用）
	// D-469a S4：私有 WS 事件 kick 通道（main() 创建共享，WS hooks 侧非阻塞 send）。
	incomeKickCh, okxIncomeKickCh, ioKickCh chan struct{}
	// D-469c S3：facts 采集完成通知总线（sched.AfterRun 发布源名；runServices 订阅→swapArb factsKick）。
	factsDone  *bus.Topic[string]
	configKick *bus.Topic[struct{}] // D-469b S2：config mutation 共享 kick
	posChange  *bus.Topic[struct{}] // D-571 包C：positions 表写点 kick（posMon 订阅）
	probeTopic *bus.Topic[struct{}] // D-163：探针提案机审 kick（explorer 落库发布端）
	// xassetAllow D-017 R1：detector 白名单前置滤→atEngine 热重载 holder 延迟绑定
	//（runServices 内 atEngine 装配后 Bind 接通；未接通 boot 窗口=零候选 fail-closed）。
	xassetAllow *xassetAllowRef
}
