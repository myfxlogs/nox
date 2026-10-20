// wire_alert_sweep.go —— 告警扇出扫带（SLO 票⑤⑥钦定径：fanout_pending 判据列——
// 内插径 hook 推/抑=false；进程外摄入件（alert-emit 族）hook=nil→true 待扫带。
// 扫带捞 pending 行→fn 扇出→Clear 封口：外源唯一推、内源单推、重启幂等天然
// （崩溃漏清=重推至多一倍——比漏推强；无游标/播种/回放风暴面）。
package main

import (
	"context"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// startAlertFanoutSweep 注册扫带 GoLoop（fn=SetAlertHook 同一扇出闭包——
// 5s 拍：alert-emit 落库→扫带捞→feishu 即达，「emit 后 30s 内」锚内）。
func startAlertFanoutSweep(ctx context.Context, st *pgstore.Store, fn func(pgstore.Alert)) {
	safego.GoLoop("main:alertFanoutSweep", func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweepPendingAlerts(ctx, st, fn)
			}
		}
	})
}

// sweepPendingAlerts 单轮 pending 扫（捞败=本轮跳过下轮重扫；fn 后逐条清位）。
func sweepPendingAlerts(ctx context.Context, st *pgstore.Store, fn func(pgstore.Alert)) {
	list, err := st.AlertsPending(ctx)
	if err != nil || len(list) == 0 {
		return
	}
	for _, a := range list {
		fn(a)
		_ = st.ClearAlertFanout(ctx, a.ID)
	}
}

// wireAlertFanout 扇出 fn 单源——内插 hook 与扫带环回共喂（main() 抽件保行数）。
func wireAlertFanout(ctx context.Context, st *pgstore.Store, alertTopic *bus.Topic[pgstore.Alert], feishuPush, bridgePush func(pgstore.Alert)) {
	fanout := func(a pgstore.Alert) {
		alertTopic.Publish(a)
		feishuPush(a)
		bridgePush(a)
	}
	st.SetAlertHook(fanout)
	startAlertFanoutSweep(ctx, st, fanout)
}
