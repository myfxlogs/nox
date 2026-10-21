// wire_kicks.go —— kick 事件 topic 装配族（自 main_helpers.go 迁出，D-093 行数纪律，
// 纯移动零语义变更）：factsDone→swapArb kick / configKick+posChange 双 topic / positions 写点回调。
package main

import (
	"context"

	"nox/internal/data/bus"
	"nox/internal/platform/safego"
	"nox/internal/risk/position"
	"nox/internal/store/pgstore"
	"nox/internal/trading/autotrade"
)

// wireSwapArbFactsKick D-469c S3：facts 写后 → swapArb factsKick（只挂喂 swap 评估的源：
// mt_swap + 三所 WS funding 流（D-518 REST 退役）；其他源完成与 swap 组合无关不 kick）。
func wireSwapArbFactsKick(ctx context.Context, factsDone *bus.Topic[string], atEngine *autotrade.Engine) {
	if factsDone == nil {
		return
	}
	safego.GoLoop("setup:atEngine.factsKick", func() {
		fdCh, fdCancel := factsDone.Subscribe()
		defer fdCancel()
		for {
			select {
			case <-ctx.Done():
				return
			case name, ok := <-fdCh:
				if !ok {
					return
				}
				switch {
				case name == "mt_swap", wsFundingSrcs[name]: // D-518 R6：WS src 精确集（REST 裸名随采集器退役删除）
					atEngine.KickSwapArb()
					atEngine.KickCarryEval() // D-004 P1#4：carry 退出判据输入变了 → positionLoop 权威全扫
				}
			}
		}
	})
}

// newKickTopics D-469b S2/S3：双事件 topic 创建——
//   - configKick：config 类 mutation 共享 kick（exchange/allow/watchlist/priceAlert RPC →
//     publish；三 cex manager+热载器+priceAlert 订阅扇出；带外 SQL 改库由 ticker 兜底）；
//   - posChange：positions 表变更（pgstore.OnPositionChange → publish；position/personal
//     monitor 订阅即刷，5min 自愈兜底）。
func newKickTopics() (configKick, posChange *bus.Topic[struct{}]) {
	return bus.NewTopic[struct{}](), bus.NewTopic[struct{}]()
}

// wirePositionKicks D-469b S3：positions 写点统一回调+两 monitor kick 订阅。
// D-633 钱径池同接（money-pool-store-no-poschange-kick 坑件）：pipeline 全量
// positions 写点走 stMoney——不接则 ClosePosition/CreatePositionLeg 等提交
// 零 kick，posreview/posMon 对钱径写盲（空仓段 pos#1 平仓 25min 零 review 实证）。
func wirePositionKicks(st, stMoney *pgstore.Store, posMon *position.Monitor, posChange *bus.Topic[struct{}]) {
	posMon.SetKickTopic(posChange)
	kick := func() { posChange.Publish(struct{}{}) }
	st.OnPositionChange = kick
	stMoney.OnPositionChange = kick
}

// mustSub D-469b：topic 订阅端简写（cancel 由订阅方 goroutine 生命周期托管——服务级常驻订阅不取消）。
func mustSub(t *bus.Topic[struct{}]) <-chan struct{} {
	ch, _ := t.Subscribe()
	return ch
}
