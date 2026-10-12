// service_combo_verdict_mt.go —— D-076：MT 腿池快照过期时的判定时刻实读。
// MT 空仓账户无账户流帧（mtapi 仅 OnOrderProfit 推帧，10-05 实拍平掉最后一仓后零帧），
// venue_account_latest 快照仅在 AccountMonitor 被 kick 时刷 → 闲置 >5min 恒「过期」，
// 展示面与执行面（marginFloorEval/legPoolSizing 均走 mtAcctSummary 实读）口径不一。
// 改法=与执行链同口径：判定时刻实读 RPC（D-563 S4b / D-071 B 同例外：零持仓无帧为
// venue 行为，读的是 venue 当前值非快照）；顺发定向 kick 让快照面随之刷新。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/data/keydomain"
)

// mtPoolKickGuard MT 池实读顺带 kick 的 per-vk 节流窗（合批轮次秒级，防连踢打 venue）。
const mtPoolKickGuard = 30 * time.Second

// mtPoolLive 快照过期的 MT 腿池值改判定时刻实读（D-076）。
// 返回 (池值, unknown注记, handled)：handled=false=非 MT 键（走原快照龄闸）；
// handled=true+注记空=实读成功继续 need vs avail 判定；注记非空=RPC 败 fail-closed。
func (s *Service) mtPoolLive(ctx context.Context, vk string, p allocPool) (allocPool, string, bool) {
	if !keydomain.IsMTKey(vk) {
		return p, "", false
	}
	// 定向 kick（无防抖路径）让快照面随之刷新；per-vk ≥30s 守卫防合批轮连踢。
	if s.acctMon != nil {
		s.mtPoolKickMu.Lock()
		if s.mtPoolKick == nil {
			s.mtPoolKick = map[string]time.Time{}
		}
		if t, ok := s.mtPoolKick[vk]; !ok || time.Since(t) >= mtPoolKickGuard {
			s.mtPoolKick[vk] = time.Now()
			s.acctMon.Kick(vk)
		}
		s.mtPoolKickMu.Unlock()
	}
	eq, fm, ccy, err := s.mtAcctSummary(ctx, vk)
	if err != nil {
		return p, fmt.Sprintf("%s（MT 实时资金不可得：%v）", vk, err), true
	}
	return allocPool{key: vk, equity: eq, free: &fm, currency: ccy, at: time.Now(), live: true, src: "rpc"}, "", true
}
