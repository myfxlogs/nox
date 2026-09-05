// D-234 CMC 看盘实时化：帧钩子 seam（FrameKind 类型 + SetFrameHook + fireFrameHook）。
package cmc

// FrameKind D-234：CMC ws 帧种类标签——帧钩子按帧类型通知 dashboard 蓄压重算。
type FrameKind int

const (
	FramePosition FrameKind = iota + 1 // 持仓帧（type5 交叉校验流）
	FrameAccount                       // 账户摘要帧（type6 主数据源+落库）
	FramePrice                         // 价格帧（type7 持仓+价格 patch）
)

// SetFrameHook D-234：注入帧钩子（main 装配；nil=未接线→fireFrameHook 静默返回）。
func (w *WsClient) SetFrameHook(fn func(FrameKind)) { w.frameHook = fn }

// fireFrameHook D-234：落库后通知 dashboard（先落库后通知语义）。
// recover 保护——通知 panic 不影响落库结果（通知失败不影响落库，硬约束⑦）。
func (w *WsClient) fireFrameHook(kind FrameKind) {
	if w.frameHook == nil {
		return
	}
	defer func() { _ = recover() }()
	w.frameHook(kind)
}
