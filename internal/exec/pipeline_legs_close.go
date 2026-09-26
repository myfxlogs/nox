package exec

import (
	"context"
	"errors"
	"fmt"
	"log"

	"arb/internal/store/pgstore"
)

// ClosePosition 平仓（§13.3）。
// D-068 修复：MT5 hedging 模式下 PlaceOrder 反向下单 = 开新仓不是平仓。
// MT5/MT4 腿用 CloseOrder（OrderClose 按 ticket 平仓）；CEX 腿用 PlaceOrder 反向减仓（D-059）。
//
// D-189 R2 修复：ClosePosition 改为 lockClose → ClaimClose → closePositionCore 三行委托，
// 删除原重复逻辑（legs 遍历/下单/残量/PnL/完整性校验），全部由 closePositionCore 统一处理。
// 消除两条平行平仓路径：ClosePosition 与 ResumeClose 共用同一核心，享有相同的
// per-position 锁/brokerPreflight/历史回填/现金流补齐/LegNo 去重。
func (p *Pipeline) ClosePosition(ctx context.Context, posID int64) error {
	return p.closePosition(ctx, posID, false)
}

// ClosePositionForce D-495 S5：业主显式逃逸门——明知单腿裸露也要平的强制平仓
// （dashboard RPC force=true 唯一入口）。跳过 ClaimClose 前置可平性闸，其余同 ClosePosition。
func (p *Pipeline) ClosePositionForce(ctx context.Context, posID int64) error {
	return p.closePosition(ctx, posID, true)
}

// closePosition D-495 S4：force=false 时 ClaimClose 之前跑全腿可平性闸——
// 首平阻断必须在 claim 之前，否则翻 CLOSING 后只能靠 ResumeClose 空转。
// 任一腿不可行动 → ErrLegNotActionable，position 保持 OPEN 零状态污染。
func (p *Pipeline) closePosition(ctx context.Context, posID int64, force bool) error {
	// D-189 P0-4：per-position close 锁（与 ResumeClose 共用）
	unlock := p.lockClose(posID)
	defer unlock()

	if !force {
		if err := p.checkLegsActionable(ctx, posID); err != nil {
			return err
		}
	}

	// D-159 W1a R3-1：原子领取平仓权（只允许 OPEN/LEGGED→CLOSING，防并发重复领取）。
	if err := p.store.ClaimClose(ctx, posID); err != nil {
		if errors.Is(err, pgstore.ErrClaimFailed) {
			log.Printf("[exec] position %d close claim failed (not OPEN/LEGGED, concurrent claim?): %v", posID, err)
			return err
		}
		log.Printf("[exec] position %d ClaimClose: %v", posID, err)
		return fmt.Errorf("claim close position %d: %w", posID, err)
	}
	// D-189 R2：进入与 ResumeClose 相同的公共执行核心。
	// closePositionCore 不调 ClaimClose（已由调用方完成）；不重复发单/写 leg（legNo 去重）。
	return p.closePositionCore(ctx, posID, force)
}
