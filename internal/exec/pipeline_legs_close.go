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
	// D-189 P0-4：per-position close 锁（与 ResumeClose 共用）
	unlock := p.lockClose(posID)
	defer unlock()

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
	return p.closePositionCore(ctx, posID)
}
