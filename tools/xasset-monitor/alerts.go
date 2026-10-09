// D-335 拆分：飞书告警段（自 main.go 移出保行预算）。
package main

import (
	"context"
	"fmt"
	"log"

	"arb/internal/notify"
	"arb/internal/store/pgstore"
)

// alertItem 告警项。
type alertItem struct {
	name string
	val  float64
	thr  float64
	op   string // "<" 或 ">"
}

func checkAlerts(entrySpread, exitSpread, entryNet, fundingAnnual, fundingRate, trackingErr, equivAnnual float64) []alertItem {
	var out []alertItem
	if entrySpread < 10 {
		out = append(out, alertItem{"进场价差不足", entrySpread, 10, "<"})
	}
	if exitSpread > 10 {
		out = append(out, alertItem{"平仓成本高", exitSpread, 10, ">"})
	}
	if entryNet < 5 {
		out = append(out, alertItem{"进场无利", entryNet, 5, "<"})
	}
	if fundingAnnual < 1.0 {
		out = append(out, alertItem{"funding 年化过低", fundingAnnual, 1.0, "<"})
	}
	if fundingRate < 0 {
		out = append(out, alertItem{"funding 单次负值", fundingRate, 0, "<"})
	}
	if trackingErr > 50 {
		out = append(out, alertItem{"跟踪误差过大", trackingErr, 50, ">"})
	}
	if equivAnnual < 2.0 {
		out = append(out, alertItem{"等效年化低于基准", equivAnnual, 2.0, "<"})
	}
	return out
}

func sendFeishuAlert(ctx context.Context, st *pgstore.Store, alerts []alertItem, rec *pgstore.XassetMonitor) {
	// D-145：凭据只认 app_secrets（env 已退役，run.sh 置空——与 pushFatalFeishu 同轨）。
	secrets, serr := st.LoadAppSecrets(ctx)
	if serr != nil {
		log.Printf("xasset-monitor: app_secrets 读取失败，告警跳过（%d 项）: %v", len(alerts), serr)
		return
	}
	appID, appSecret := secrets["ARB_FEISHU_APP_ID"], secrets["ARB_FEISHU_APP_SECRET"]
	fc := notify.NewFeishuClient(appID, appSecret)
	if appID == "" || appSecret == "" || !fc.Enabled() {
		log.Printf("xasset-monitor: 飞书未配置/未启用，告警跳过（%d 项）", len(alerts))
		return
	}
	chatID, err := st.FeishuChatID(ctx)
	if err != nil || chatID == "" {
		log.Printf("xasset-monitor: chat_id 读取失败/未绑定，告警跳过（%d 项）", len(alerts))
		return
	}
	text := fmt.Sprintf("[%s 监控] %d 项告警\n", rec.Pair, len(alerts))
	for _, a := range alerts {
		text += fmt.Sprintf("• %s: %.4f (阈值 %s %.4f)\n", a.name, a.val, a.op, a.thr)
	}
	text += fmt.Sprintf("\n当前：进场价差 %.2fbps / 进场净收益 %.2fbps / funding 年化 %.2f%% / 跟踪误差 %.2fbps / 等效年化 %.2f%%",
		rec.EntrySpreadBps, rec.EntryNetBps, rec.FundingAnnualPct, rec.TrackingErrorBps, rec.EquivAnnualPct)
	if err := fc.SendText(ctx, chatID, text); err != nil {
		log.Printf("xasset-monitor: 飞书告警发送失败: %v", err)
		return
	}
	log.Printf("xasset-monitor: 飞书告警已推送（%d 项）", len(alerts))
}
