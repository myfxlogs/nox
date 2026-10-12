// binance_user_ws_reconnect.go D-591 S1：用户流重连节拍域自 binance_user_ws.go 拆出
// （check-lines ≤300 同 spot/balance 拆分模式）——Run 循环+封禁窗感知退避。
package exchange

import (
	"context"
	"log"
	"math/rand"
	"time"

	"arb/internal/infra/dephealth"
	"arb/internal/platform/alerttext"
)

// banJitter D-591 S1：封禁到期后重连抖动界 0..N 随机（测试可缩）。
var banJitter = 30 * time.Second

// Run 重连循环（常规错误 5s 退避）。D-591 S1：listenKey POST 遇 -1003/418 →
// 退避至封禁到期+抖动，窗内零 REST——盲 5s 锤是封禁诱因放大器且窗内续期封禁
// （S0 实证 04:10:50→04:14:51）。每次迭代取新 listenKey（官方语义：失效不复用）。
func (b *BinanceUserWS) Run(ctx context.Context) {
	var backoffUntil time.Time
	for {
		if err := b.runOnce(ctx); err != nil {
			dephealth.Mark(b.depKey(), false, err) // D-579 R2：私有流自挂点（spot 支路同经此）
			log.Printf("[binance-user-ws] reconnect after error: %v", err)
			if until, ok := alerttext.BannedUntil(err.Error()); ok && until.After(b.now()) {
				backoffUntil = until.Add(time.Duration(rand.Int63n(int64(banJitter) + 1)))
			}
		} else {
			backoffUntil = time.Time{} // 连接成功清退避
		}
		wait := 5 * time.Second
		if !backoffUntil.IsZero() && backoffUntil.After(b.now()) {
			wait = backoffUntil.Sub(b.now()) // 封禁窗睡到到期（短封禁也按到期节拍）
		} else {
			backoffUntil = time.Time{} // 到期（或未置）恢复常规节奏
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
