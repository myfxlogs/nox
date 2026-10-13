// autoexec.go —— D-260 M4.2 ④：live 策略自动执行并入 oppmatch 扫描拍（零新定时器）。
//
// 闸序（任缺=fail-closed 静默跳过，不产生告警噪音）：
//  1. 提案 status='proposed' 且模板∈ExecTemplates（可执行族）
//  2. fin_strategy 键=template_id 且 state=='live'——行缺/非 live/读败皆拒
//     （休眠轨/过期提案同此：非 live 不点火）
//  3. fin_opp_eval 评估卡 verdict=='viable'（M4.1 卡闸，GoProposal 内复验）
//  4. GoProposal 既有闸序：态机+凭据+realtime 报价+风险约束全保留
//
// 首次晋级 live 只告警不执行=迁移侧保证（SetStrategyState 提示文案）；
// 本函数只看「当前态=live」——晋级当拍若恰遇扫描仍会执行，属票面预期
// （晋级即授权，非「冷启动缓冲」）。
package dciexec

import (
	"context"
	"fmt"
	"log"

	"nox/internal/risk/gate"
)

// AutoExecLive 扫描拍内自动执行（oppmatch RunOnce 后同拍调用）。
// 返回执行数；单项失败 log+续跑（提案独立不误伤）。
func (e *Executor) AutoExecLive(ctx context.Context) (int, error) {
	props, err := e.Store.ListProposals(ctx, "proposed", 50)
	if err != nil {
		return 0, fmt.Errorf("autoexec list: %w", err)
	}
	fired := 0
	for _, p := range props {
		if !gate.IsExecTemplate(p.TemplateID) {
			continue // 非可执行族模板跳过
		}
		state, ok, err := e.Store.GetStrategyState(ctx, p.TemplateID)
		if err != nil || !ok || state != "live" {
			continue // 状态机 fail-closed
		}
		msg, err := e.GoProposal(ctx, p.ID)
		if err != nil {
			log.Printf("[autoexec] proposal #%d %s: %v（拒单留痕 GoProposal 内）", p.ID, p.TemplateID, err)
			continue
		}
		fired++
		log.Printf("[autoexec] proposal #%d %s live 自动执行：%s", p.ID, p.TemplateID, msg)
	}
	return fired, nil
}
