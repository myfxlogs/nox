// wheel.go —— D-191：轮轨执行+托管池余额读面（自 exec.go 拆分，纯移动零语义改）。
package dciexec

import (
	"context"
	"fmt"
	"strings"
)

// GoWheel 轮式双腿（旧 Go 全腿径保留为内部径——D-191 R3 wheel 语义；
// 提案绑定径=GoProposal，无提案场景此径不再口令可达）。
func (e *Executor) GoWheel(ctx context.Context) (string, error) {
	if e.BnKey == "" {
		return "", e.gap(ctx, callLeg, "credential", "binance:spot",
			"请配置 binance 凭据（DCI 申购写径）")
	}
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return "", err
	}
	share, tenorCap := tenorCapOf(cons)
	if share == nil {
		return "", e.gap(ctx, callLeg, "constraint", "custody_share",
			"请喂约束 custody_share（托管可动份额上限）")
	}
	// 现货执行时实时读（strike %OTM 闸源）。
	spot, err := e.spotPrice(ctx, "BTCUSDT")
	if err != nil {
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, callLeg, "permission", "dual_quote",
				"binance BTC 现货价读取失败：%v", err)
		}
		return "", e.gap(ctx, callLeg, "runtime", "dual_quote",
			"系统侧病灶：BTC 现货价读取失败：%v", err)
	}
	var msgs []string
	cb, pb := OTMBoundRatios(cons)
	for _, l := range []leg{callLeg, putLeg} {
		ratio := l.ratioOf(cb, pb)
		if ratio == nil {
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.name,
				e.gap(ctx, l, "constraint", l.boundSubject(),
					"请喂约束 %s（%s腿 OTM 闸界比率）", l.boundSubject(), l.name)))
			continue
		}
		msg, err := e.tryLeg(ctx, l, *share, tenorCap, spot*(*ratio), spot, nil)
		if err != nil {
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.name, err))
			continue
		}
		msgs = append(msgs, msg)
	}
	return strings.Join(msgs, "；"), nil
}

// poolBalance 托管池余额=现货(free+locked)+赚币子账持仓（R6 池口径；
// outstanding DCI 锁仓在对账侧回填，下单 qty 不含——在外单不再复投）。
func (e *Executor) poolBalance(ctx context.Context, asset string) (float64, error) {
	bal, err := e.freeBalance(ctx, asset)
	if err != nil {
		return 0, err
	}
	eps, err := e.earnPositionsOf(ctx, asset)
	if err != nil {
		return 0, err
	}
	for _, p := range eps {
		bal += p.Amount
	}
	return bal, nil
}

func (e *Executor) earnPositionsOf(ctx context.Context, asset string) ([]EarnPosition, error) {
	if e.EarnFn != nil {
		return e.EarnFn(ctx, asset)
	}
	return e.earnPositions(ctx, asset)
}
