// wire_gate.go —— D-570 包C 装配面（自 main_monitors.go 拆出保行数）：
// gate contract_info 失效闭包 + 风险频道族 hooks 注入。
package main

import (
	"nox/internal/api/dashboard"
	"nox/internal/data/collect/exchange"
	"nox/internal/ledger/reconcile"
	"nox/internal/trading/exec"
)

// wireGateStreamHooks D-570 包C S6⑤/S7：OnContractInfo→exec spec+collect quanto 双失效；
// 风险频道族全整帧 critical（payload=result 原文截 500；auto_deleverages/position_adl_rank 同槽 OnADL）。
// D-015 ②（R1）：position_closes/liquidates/ADL 告警保留 + recon.Kick 叫醒对账，
// 不再直写 CLOSE 腿（okx/binance 平仓频道同接，装配见 wire_venue_close.go）。
func wireGateStreamHooks(gateWS *exchange.GateWebSocket, mgr *dashboard.CexStreamManager, cexAlertFn func(posID *int64, level, msg string), recon *reconcile.Reconciler) {
	gateCrit := func(tag string) func([]byte) {
		return func(payload []byte) {
			msg := string(payload)
			if len(msg) > 500 {
				msg = msg[:500]
			}
			cexAlertFn(nil, "critical", "gate "+tag+" 风险事件\n原始明细："+msg)
		}
	}
	mgr.GateHooks = exchange.GatePrivateHooks{
		OnLiquidate:     gateCloseEventFn(recon, gateCrit("liquidates")),
		OnPositionClose: gateCloseEventFn(recon, gateCrit("position_closes")),
		OnADL:           gateCloseEventFn(recon, gateCrit("adl")),
		OnRiskLimit:     gateCrit("reduce_risk_limits"),
	}
	if gateWS != nil {
		gateWS.SetOnContractInfo(func(contract string) {
			exec.GateSpecInvalidate(contract)
			mgr.GateInvalidateQuanto(contract)
		})
	}
}
