package main

import (
	"context"
	"log"
	"strings"

	"nox/internal/ir/notify"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// wireBridgePush 告警桥 sink 装配——与 feishuPush 并列写出层（旁路零阻塞，
// fail-open：桥败不影响主径）。键读皆热：url/patterns 走 app_config
// AppConfigString（缺=静默关 fail-closed），token 走 secRes（app_secrets）。
func wireBridgePush(ctx context.Context, st *pgstore.Store, secRes *config.SecretResolver) *notify.AgentBridgeSink {
	sink := notify.NewAgentBridgeSink(
		func() string {
			v, ok, err := st.AppConfigString(ctx, "NOX_ALERT_BRIDGE_URL")
			if err != nil || !ok {
				return ""
			}
			return v
		},
		func() string { return secRes.Get("NOX_ALERT_BRIDGE_TOKEN") },
		func() []string {
			v, ok, err := st.AppConfigString(ctx, "NOX_ALERT_BRIDGE_WARN_PATTERNS")
			if err != nil || !ok || v == "" {
				// 缺省三族起手（设计稿 §7.4）——[前提塌]/断流 stale/attach 超时
				return []string{"[前提塌]", "[stale]", "[attach]"}
			}
			return strings.Split(v, ",")
		},
	)
	safego.GoLoop("main:bridge-push", func() { sink.Run(ctx) })
	// O1 活性看门狗：每 24h 落一行 digest 料（sink 计数+桥在线实读——活性
	// 自证防静默失明；digest 06:10 批读 alerts 表无桥专行→走 log 面兜底）。
	return sink
}

// bridgePushFor 告警 hook 适配——pgstore.Alert 转 sink.Push 参。
func bridgePushFor(sink *notify.AgentBridgeSink) func(pgstore.Alert) {
	return func(a pgstore.Alert) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[bridge] push panic（静默吞不炸主径）: %v", r)
			}
		}()
		sink.Push(a.ID, a.Level, a.Message)
	}
}
