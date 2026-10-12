// wire_macro.go —— D-528 S8：宏观采集层装配。D-545 热闸化：env 早退闸移除=常开 goroutine，
// 每班唤醒判 secRes.Get("NOX_MACRO_ENABLED")（UpdateAppConfig 热推即效免重启）；
// 密钥经 resolver 热读（UpdateAppSecret 即效）。D-147：推送收口业主域——财历/actuals
// 仅 alerts 留痕，不再直推飞书（推送面随 Deps.Notify/ChatID 摘除）。
package main

import (
	"context"
	"log"

	"nox/internal/data/macro"
	"nox/internal/ir/notify"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// startMacro 装配并启动宏观调度 goroutine（D-545：热闸化——常开 goroutine、每班唤醒判
// secRes.Get("NOX_MACRO_ENABLED")，false=当班跳过；UpdateAppConfig 热推即效免重启）。
// D-168 ⑤：日历日报恢复直推（actuals 仍 info 留痕不推）。
func startMacro(ctx context.Context, st *pgstore.Store, secRes *config.SecretResolver, feishu *notify.FeishuClient) {
	safego.Go("main:macro", func() {
		macro.Run(ctx, macro.Deps{
			Store:   st,
			Enabled: func() bool { return secRes.Get("NOX_MACRO_ENABLED") == "true" },
			Notify:  feishu,
			ChatID:  st.FeishuChatID,
		})
	})
	log.Printf("[macro] started (hot gate via NOX_MACRO_ENABLED): calendar 07:20/07:30BJ")
}
