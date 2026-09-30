// reporter.go —— D-558 S2：启动致命错误上报通道 + 端点同源装载助手。
// 与 tick 价差告警（sendFeishuAlert，env 凭据静音）完全独立：fatal 走 app_secrets
// 凭据直推（run.sh 置空 ARB_FEISHU_* 只影响 tick 通道，本条不被误静音）。
// 尽力而为：任一步败只 log，绝不 panic/改写原 fatal error；st=nil（pg 未连上）优雅退化只 log。
// 30min 同类去重查 alerts 表（cron 每 5min 独立进程，内存态不持久——必须查库）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"arb/internal/alerttext"
	"arb/internal/config"
	"arb/internal/notify"
	"arb/internal/store/pgstore"
)

// binanceProxyURL D-558 S1：BINANCE_PROXY_URL 与 core 同源——LoadAppConfig→
// ApplyAppConfigEnvOverlay（DB 行覆盖 env 值）。LoadAppConfig 败且 env 非空→
// warn 明示临时回退 env；双缺（app_config 无行+env 未设）→ error（含 app_config 字样）。
func binanceProxyURL(ctx context.Context, st *pgstore.Store) (string, error) {
	rows, err := st.LoadAppConfig(ctx)
	if err != nil {
		if v := os.Getenv("BINANCE_PROXY_URL"); v != "" {
			log.Printf("xasset-monitor: app_config 读取失败，临时回退 env BINANCE_PROXY_URL: %v", err)
			return v, nil
		}
		return "", fmt.Errorf("app_config 读取失败且 env 无 BINANCE_PROXY_URL: %w", err)
	}
	config.ApplyAppConfigEnvOverlay(rows)
	if v := os.Getenv("BINANCE_PROXY_URL"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("BINANCE_PROXY_URL 未配置（app_config 无行且 env 未设），监控簿断更=待入场组合判过期")
}

// reportFatal S2 单点捕获：fatal err → alerts critical 行 + app_secrets 飞书直推。
// 30min 内同类已推过 → 只落行不推飞书（去重）；首发生/超窗 → 落行+推。
func reportFatal(ctx context.Context, st *pgstore.Store, err error) {
	if err == nil {
		return
	}
	if st == nil {
		log.Printf("xasset-monitor: fatal 未持久化（无 store）: %v", err)
		return
	}
	// D-592 S2：直推旁路同 key 对齐——飞书推送判定与 insertAlert 漏斗共用归一键
	// （原前缀式对异型 fatal 过抑制；仅改查重，文案链不动）。
	msg := "xasset-monitor " + alerttext.VenueErrCN(err.Error())
	dup := hasRecentSameKeyAlert(ctx, st, "critical", msg)
	if aerr := st.InsertAlert(ctx, nil, "critical", msg); aerr != nil {
		log.Printf("xasset-monitor: fatal alert 落库失败: %v", aerr)
	}
	if dup {
		log.Printf("xasset-monitor: fatal 30min 内同类已推，去重跳飞书: %v", err)
		return
	}
	pushFatalFeishu(ctx, st, err)
}

// hasRecentSameKeyAlert D-592 S2：30min 内同键 critical 在档判定（键=alerttext 归一化
// 与 insertAlert 漏斗同源；候选按前缀收窄）。查询失败=按未推过处理（fail-open 同 D-592）。
func hasRecentSameKeyAlert(ctx context.Context, st *pgstore.Store, level, message string) bool {
	key := alerttext.AlertDedupKey(level, message)
	rows, err := st.Pool().Query(ctx,
		`SELECT message FROM alerts WHERE level=$1 AND message LIKE 'xasset-monitor%' AND ts > now()-interval '30 minutes'`, level)
	if err != nil {
		log.Printf("xasset-monitor: fatal 去重查询失败（按未推过处理）: %v", err)
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		if rows.Scan(&m) == nil && alerttext.AlertDedupKey(level, m) == key {
			return true
		}
	}
	return false
}

// pushFatalFeishu 飞书直推——凭据只认 app_secrets（ARB_FEISHU_* env 已被 run.sh 置空，
// env 回退=永假死路）；未配置/未绑定/发送败各只 log 不 escalate。
func pushFatalFeishu(ctx context.Context, st *pgstore.Store, fatalErr error) {
	secrets, err := st.LoadAppSecrets(ctx)
	if err != nil {
		log.Printf("xasset-monitor: fatal 飞书推送跳过（app_secrets 读取失败: %v）", err)
		return
	}
	fc := notify.NewFeishuClient(secrets["ARB_FEISHU_APP_ID"], secrets["ARB_FEISHU_APP_SECRET"])
	if !fc.Enabled() {
		log.Printf("xasset-monitor: fatal 飞书推送跳过（app_secrets 凭据未配置）")
		return
	}
	chatID, err := st.FeishuChatID(ctx)
	if err != nil || chatID == "" {
		log.Printf("xasset-monitor: fatal 飞书推送跳过（chat_id 未绑定/读取失败: %v）", err)
		return
	}
	if err := fc.SendText(ctx, chatID, "xasset-monitor 致命："+alerttext.VenueErrCN(fatalErr.Error())); err != nil { // D-588：直推旁路同款中文化（不走 alerts 漏斗）
		log.Printf("xasset-monitor: fatal 飞书发送失败: %v", err)
		return
	}
	log.Printf("xasset-monitor: fatal 已推飞书: %v", fatalErr)
}
