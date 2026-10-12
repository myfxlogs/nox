// wire_venue_close.go —— D-015 ②（R1）：venue 平仓/强平/ADL 推送事件 → recon.Kick。
// 事故锚：#505 gate 腿 15:55 已平（position_closes text=t-arb），系统 15:59–16:35
// 仍报「券商侧已无此腿」×7——事件只告警没进状态机。
// R1 修订：帧不再直写 CLOSE 腿——WS 帧可能先于本系统平仓回报到达（自发平仓
// position_closes 同为该帧），抢先落 EXTERNAL 会占住 attempt 序位、令权威 FILLED
// 无法落库、价格/费用永远缺失。帧只做「叫醒对账」；外部平仓判定收敛到
// pipeline_resume_helpers 历史回填无命中分支，查 cex_private_events 平仓帧兜底。
package main

import (
	"nox/internal/api/dashboard"
	"nox/internal/ledger/reconcile"
)

// wireVenueCloseHooks okx/binance 平仓事件 hook 装配（main_monitors 拆出保行数）：
// positions pos=0 / ACCOUNT_UPDATE pa=0 → recon.Kick。
func wireVenueCloseHooks(mgr *dashboard.CexStreamManager, recon *reconcile.Reconciler) {
	kick := func(string, string, string) { kickRecon(recon) }
	mgr.OKXHooks.OnPositionClosed = kick
	mgr.BinanceHooks.OnPositionClosed = kick
}

// kickRecon nil 安全的叫醒（recon 装配在主线恒非空，兜底防装配顺序变更）。
func kickRecon(recon *reconcile.Reconciler) {
	if recon != nil {
		recon.Kick()
	}
}

// gateCloseEventFn gate 平仓域帧处理（position_closes/liquidates/ADL）：
// critical 告警保留（原 wire_gate.go gateCrit 语义）+ recon.Kick 叫醒对账。
// 不解析行、不落库——帧全文已审计入 cex_private_events 供回填兜底查证。
func gateCloseEventFn(recon *reconcile.Reconciler, critFn func([]byte)) func([]byte) {
	return func(payload []byte) {
		critFn(payload)
		kickRecon(recon)
	}
}
