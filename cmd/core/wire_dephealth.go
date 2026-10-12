package main

import (
	"context"
	"log"
	"strings"

	"nox/internal/infra/dephealth"
	"nox/internal/platform/alertcool"
	"nox/internal/risk/selfwatch"
	"nox/internal/store/pgstore"
)

// egressAggregated D-104②：canary 红窗内 ws-storm/面判定劣化告警聚合为 canary
// 单条 critical（红窗根因=本机出口，分面告警并作一条降噪；面板见 canary 条目）。
// 红窗态=selfwatch 进程内 canary 拍间原子——canary 停跑/重启间隙读 false=放行
// （fail-open 宁多告不漏告）。
func egressAggregated(msg string) bool {
	if !selfwatch.EgressRed() {
		return false
	}
	return strings.Contains(msg, "[ws-storm]") || strings.Contains(msg, "面判定劣化")
}

// wireDephealth D-579 S6：dephealth 双向变迁告警道（degraded=warn/恢复=info；
// 同指纹冷却收口，镜像 cexAlertFn 装配形态）。D-104②：红窗内「面判定劣化」聚合进
// canary 单条 critical（恢复 info 不受闸影响——恢复与红窗并存时仍如实记档）。
func wireDephealth(st *pgstore.Store, cooler *alertcool.Cooler) {
	dephealth.Init(cooler.Wrap1(func(level, msg string) {
		if egressAggregated(msg) {
			log.Printf("[egress] canary 红窗聚合告警: %.80s", msg)
			return
		}
		if err := st.InsertAlert(context.Background(), nil, level, msg); err != nil {
			log.Printf("[dephealth] insert alert: %v", err)
		}
	}))
}
