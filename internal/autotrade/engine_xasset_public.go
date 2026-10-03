// D-340 S2：XASSET 净 carry 公开包装（零逻辑复制）——dashboard 持仓年化对照列数据源。
// 仿 DiscoverSwapCombosPublic 先例（engine_swaparb_public.go）：直调私有 recalcXAssetNetCarry
// （与引擎退出判定同源 P3 单口径——live 值与「该不该平」判定同病同药）。
package autotrade

import "context"

// XAssetNetCarryPublic 返回持仓当前净 carry（年化百分点）。
// 内部经 posID 取仓后直调 recalcXAssetNetCarry（fail-closed：缺 facts/过期/非息差型=ok=false）。
// window=0 瞬时口径——展示路径按 D-027 裁定保留瞬时值（7d 均值只进 econ 判定）。
func (e *Engine) XAssetNetCarryPublic(ctx context.Context, posID int64) (float64, bool) {
	p, err := e.store.GetPosition(ctx, posID)
	if err != nil || p == nil {
		return 0, false
	}
	return e.recalcXAssetNetCarry(ctx, p, 0)
}
