// wire_mtwatch.go D-636：MT 接入层喂流看门狗——网关进程/RPC 面（票 823 收窄面就地补测）。
// 定位实证：leg unit qty（PG 点查 5s 预算）超时簇=池饿/接入层劣化的哨兵征（SG 10-20
// 实测 38/30min 簇发，与 overview true_net 巨查同窗）。断流 stale 告警已有面
// （mt-health/覆盖断供），本狗补 RPC 面维：30s 轴滑动 5min 窗计数，阈值破→warn
// 去重告警+决策席转发令；连续 3 窗→critical 升格（业主侧网关可达性疑，
// fin_condition_gap kind=executor 候立=红线归决策席裁定，本狗不代立 gap）。
// D-004 合法性：看门狗唯检测断流/劣化→告警，零产零载业务数据。
package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"nox/internal/platform/alerttext"
	"nox/internal/store/pgstore"
)

// mtRPCFaceWatch 看门狗共享计数器（resolver 超时++；轴循环消费）。
type mtRPCFaceWatch struct {
	timeouts atomic.Int64
}

// 看门狗参数（30s 轴×10 窗=5min 窗；阈值 5=持续 1/min 劣化即报，单簇 17/min 必报）。
const (
	mtWatchTick     = 30 * time.Second
	mtWatchWindows  = 10
	mtWatchThresh   = 5
	mtWatchEscalate = 3 // 连续 N 窗破阈→critical
)

// mtWatchDecide 纯判定（单测锚面）：窗和+连续破阈数→（级，去重窗，是否告警）。
func mtWatchDecide(sum int64, consec int) (level string, dedup time.Duration, fire bool) {
	if sum < mtWatchThresh {
		return "", 0, false
	}
	if consec >= mtWatchEscalate {
		return "critical", 60 * time.Minute, true
	}
	return "warn", 30 * time.Minute, true
}

// mtWatchRun 看门狗轴（safego.GoLoop 挂起；滑窗+双级告警）。
func mtWatchRun(st *pgstore.Store, w *mtRPCFaceWatch) {
	ring := make([]int64, 0, mtWatchWindows)
	consec := 0
	ticker := time.NewTicker(mtWatchTick)
	defer ticker.Stop()
	for range ticker.C {
		ring = append(ring, w.timeouts.Swap(0))
		if len(ring) > mtWatchWindows {
			ring = ring[len(ring)-mtWatchWindows:]
		}
		if len(ring) < mtWatchWindows {
			continue // 预热窗不满不评（boot 防噪，同 mt-health bootGrace 语义）
		}
		var sum int64
		for _, v := range ring {
			sum += v
		}
		level, dedup, fire := mtWatchDecide(sum, consec)
		if !fire {
			consec = 0
			continue
		}
		consec++
		tail := "PG 池饿或网关进程面（mtapi 托管服务）——喂流看门狗 D-636"
		if level == "critical" {
			tail = "连续 3 窗劣化——业主侧网关可达性疑（fin_condition_gap kind=executor 候立归决策席裁定）"
		}
		msg := fmt.Sprintf("[MT 接入] RPC 面劣化：leg unit qty 超时 %d 次/5min（%s）", sum, tail)
		msg += alerttext.SeatDispatchLine("决策", "MT 接入层处置（读 STATE 三堵之二行+skills/mt5-gateway-admin）")
		if err := st.InsertAlertDedup(context.Background(), level, msg, dedup); err != nil {
			log.Printf("[mt-watch] alert: %v", err)
		}
	}
}
