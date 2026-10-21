// wire_schema_drift.go —— 约束漂移启动自检（carry_graduations 'futures' 缺位事故根治）：
// 迁移链声明的 CHECK 终态与活库 pg_get_constraintdef 逐一对拍，漂移=warn 告警
// dedup 24h（漂移=写时咬人型暗伤，fail-loud 不 fail-hard——只告警不阻断启动，
// 阻断会让一处非致命漂移拖垮整个 boot）。
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

const schemaDriftAlertDedup = 24 * time.Hour

func reportCheckDrift(ctx context.Context, st *pgstore.Store) {
	drift, err := st.SchemaCheckDrift(ctx)
	if err != nil {
		log.Printf("[schema_drift] 自检读败（不阻断启动）: %v", err)
		return
	}
	if len(drift) == 0 {
		return
	}
	parts := make([]string, len(drift))
	for i, d := range drift {
		parts[i] = d.String()
	}
	msg := fmt.Sprintf("[约束漂移] 迁移链 CHECK 终态与活库不符 %d 处：%s", len(drift), strings.Join(parts, "；"))
	log.Print(msg)
	if err := st.InsertAlertDedup(ctx, "warn", msg, schemaDriftAlertDedup); err != nil {
		log.Printf("[schema_drift] 告警插入: %v", err)
	}
}
