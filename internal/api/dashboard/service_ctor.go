package dashboard

import (
	"context"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// SetConfigKick D-469b S2：注入 config 变更 kick 回调（main 装配 bus.Topic.Publish）。
func (s *Service) SetConfigKick(fn func()) { s.configKick = fn }

// kickConfig D-469b：mutation 成功路径尾部调用（nil 安全）。
func (s *Service) kickConfig() {
	if s.configKick != nil {
		s.configKick()
	}
}

// FrontConfig 前端展示阈值（D-077 修复⑪B N7，后端 config 单一真相）。
type FrontConfig struct {
	NotifyMinNetBps   float64
	FreshnessTTLSec   int
	EntrySpreadMinBps float64
	MinNetSwapBps     float64
	RevalidateAbsBps  float64
	RevalidateRelPct  float64
	// D-086 验证阶梯阈值（oppToProto 填 verified_level 用，与 engine 发布门同源）。
	VerifyWindowDays int
	VerifyMinSamples int
	VerifyMinRatio   float64
}

// New 创建 dashboard 服务。reviewer 为复盘触发闭包（nil 则平仓后不触发）；
// freshTTL 为报价陈旧阈值（个人持仓展示用，传 config.DetectorFreshnessTTL）；
// liqMMR 为个人持仓理论强平维持保证金率（传 config.LiqMMR）；
// fundingDropRatio 为资金费急降预警比例（传 config.FundingDropRatio）；
// swapArbEquityUseRatio 为 swap 套利名义额资金使用率系数（S6，传 config.SwapArbEquityUseRatio）；
// swapArbBareCap 为 swap 套利单腿裸露上限系数（D-097 R2，传 config.SwapArbBareCap）；
// swapArbVenueExposureCap 为单 venue SWAPARB 合计裸露上限系数（D-132 T1，传 config.SwapArbVenueExposureCap）；
// oppTopic 为新机会站内推送 topic（D-050，传 main 创建的 oppTopic）；
// bpm 为 Broker 实持仓 monitor（D-052，传 main 创建的 bpm）；
// notionalUSD 为 net_usd 换算的单笔名义（D-053，传 config.OpportunityNotionalUSD）；
// acctMon 为账户资产 monitor（D-053，传 main 创建的 acctMon）。
// alertTopic 为告警实时推送广播（D-114，传 main 创建的 alertTopic；InsertAlert → store.alertHook → alertTopic.Publish → case 3）。
// commissionFor 为 per-venue 佣金解析器（D-111 收敛型实时 bps 成本扣除，nil = commissionBps=0）。
// slippageBps 为滑点成本（D-111，slippageCost = slippageBps * 2）。
// capitalBaseUSD 为经营目标资金基数（D-123 真北行年化分母，传 cfg.CapitalBaseUSD）。
// gateChecker 须由装配层注 *gate.ControlPlane（wire_dashboard SetGateChecker）——nil=未接线全径 fail-closed（D-620ⓑ）。
func New(store *pgstore.Store, qbus *bus.QuoteBus, pipeline *exec.Pipeline, bm *BrokerManager, reviewer func(), freshTTL time.Duration, liqMMR float64, fundingDropRatio float64, swapArbEquityUseRatio float64, swapArbBareCap float64, swapArbVenueExposureCap float64, oppTopic *bus.Topic[pgstore.Opportunity], alertTopic *bus.Topic[pgstore.Alert], bpm *BrokerPositionMonitor, notionalUSD float64, acctMon *AccountMonitor, commissionFor func(string) float64, slippageBps float64, xassetCexFeeBps float64, kickFn func(string), capitalBaseUSD float64) *Service {
	return &Service{store: store, gradStore: store, qbus: qbus, pipeline: pipeline, gateChecker: nil, bm: bm, reviewer: reviewer, freshTTL: freshTTL, liqMMR: liqMMR, fundingDropRatio: fundingDropRatio, swapArbEquityUseRatio: swapArbEquityUseRatio, swapArbBareCap: swapArbBareCap, swapArbVenueExposureCap: swapArbVenueExposureCap, oppTopic: oppTopic, alertTopic: alertTopic, bpm: bpm, notionalUSD: notionalUSD, acctMon: acctMon, commissionFor: commissionFor, slippageBps: slippageBps, xassetCexFeeBps: xassetCexFeeBps, kickFn: kickFn, capitalBaseUSD: capitalBaseUSD}
}

// SetBalanceLedger/Store/AutoClosePosition/ResumeClose/ClaimClose 转发与注入面
// 见 service_setters.go（控行数拆出）。

// Settler 结算接口（D-087 §3.2，settlement.Settle 的注入面，dashboard 不依赖 settlement 包避免循环）。
type Settler interface {
	Settle(ctx context.Context, posID int64) error
}

// setter 注入面（SetSettler/SetTradeModes/.../aliasForVenueKey——见 service_setters.go 全集）
// 见 service_setters.go（从本文件拆出控行数 <300）。

// SetMoneyStore D-633 拆票：钱径保底池句柄注入（probe-close PositionNetUSD 走保底池不被读面爆量拖）。
func (s *Service) SetMoneyStore(st *pgstore.Store) { s.storeMoney = st }
