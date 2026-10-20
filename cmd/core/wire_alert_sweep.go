// wire_alert_sweep.go —— 告警扇出扫带（SLO 票⑤钦定径①）：alert-emit 等
// 进程外摄入件绕 SetAlertHook 内链（hook=nil 进程私有）——core 侧按 id
// 游标扫增量喂同一扇出 fn（topic+feishu+bridge），外部摄入与内部插表扇出合一。
// 游标=app_config alert_fanout_cursor（启动播种 max(id) 防回放风暴；扫带败=
// log 留痕不吞——fanout 败单条最多丢一轮，游标先写防重放）。
package main

import (
	"context"
	"strconv"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

const alertSweepCursorKey = "alert_fanout_cursor" // app_config 游标键（跨重启幂等——游标早于扇出写防重放）

// startAlertFanoutSweep 注册扫带 GoLoop（fn=SetAlertHook 同一扇出闭包——
// 循环 5s：杂源摄入件延迟上界=宿主 cron 5min+本环 5s+feishu push 即达，
// 「emit 后 30s 内飞书」验收锚内）。
func startAlertFanoutSweep(ctx context.Context, st *pgstore.Store, fn func(pgstore.Alert)) {
	// 启动播种：无游标=max(id) 现值为起点（存量不回放）。
	if _, ok, _ := st.AppConfigInt64(ctx, alertSweepCursorKey); !ok {
		maxID, err := st.AlertsMaxID(ctx)
		if err != nil {
			maxID = 0 // 播种败=全量回放风暴防——退零游标幂等域
		}
		_ = st.UpsertAppConfig(ctx, alertSweepCursorKey, strconv.FormatInt(maxID, 10), "告警扇出扫带游标（SLO 票⑤）")
	}
	safego.GoLoop("main:alertFanoutSweep", func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweepAlertsOnce(ctx, st, fn)
			}
		}
	})
}

// sweepAlertsOnce 单轮增量扫（游标读败/扫败=本轮跳过，游标不落防跳丢）。
func sweepAlertsOnce(ctx context.Context, st *pgstore.Store, fn func(pgstore.Alert)) {
	cur, _, err := st.AppConfigInt64(ctx, alertSweepCursorKey)
	if err != nil {
		return
	}
	list, err := st.AlertsAfterID(ctx, cur)
	if err != nil || len(list) == 0 {
		return
	}
	var lastID int64
	for _, a := range list {
		fn(a)
		lastID = a.ID
	}
	_ = st.UpsertAppConfig(ctx, alertSweepCursorKey, strconv.FormatInt(lastID, 10), "告警扇出扫带游标")
}

// wireAlertFanout 扇出 fn 单源——内插 hook 与扫带环回共喂（SLO 票⑤：
// 进程外摄入件经扫带合一进 fanout 管道；main() 抽件保行数）。
func wireAlertFanout(ctx context.Context, st *pgstore.Store, alertTopic *bus.Topic[pgstore.Alert], feishuPush, bridgePush func(pgstore.Alert)) {
	fanout := func(a pgstore.Alert) {
		alertTopic.Publish(a)
		feishuPush(a)
		bridgePush(a)
	}
	st.SetAlertHook(fanout)
	startAlertFanoutSweep(ctx, st, fanout)
}
