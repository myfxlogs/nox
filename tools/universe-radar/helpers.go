// helpers.go —— D-376 S5：env/告警渲染/腿集 helper 组自 main.go 整组移出
// （同包纯搬移零行为变更；check-lines 超线修法，D-366 digest.go 先例）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"arb/internal/notify"
	"arb/internal/store/pgstore"
)

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func launchPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// hedgeCanonicals 腿集=broker_symbol_map distinct canonical + asset_equiv enabled canonical；
// classOf=asset_equiv canonical→asset_class（告警行展示 GOLD 有腿，broker 腿无类）。
func hedgeCanonicals(ctx context.Context, st *pgstore.Store) (map[string]bool, map[string]string, error) {
	set := make(map[string]bool)
	maps, err := st.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	for _, m := range maps {
		if m.Canonical != "" {
			set[m.Canonical] = true
		}
	}
	classOf := make(map[string]string)
	eq, err := st.ListAssetEquiv(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range eq {
		set[e.Canonical] = true
		classOf[e.Canonical] = e.AssetClass
	}
	return set, classOf, nil
}

// alertLine 告警行：`[universe-radar] <EVENT> <tier> <venue> <symbol> (leg=<hedge>[ class=<GOLD>]) ann=<x>%/yr vol=$<y>`。
// fundingEventLine D-353 S1：事件式告警行——kind 标签 + ann 旧→新两侧数值
// （跌出行旧值→新值；新进行旧值多为 n/a）。
func fundingEventLine(ev fundingEvent) string {
	label := map[fundingEventKind]string{
		fundingNewEntry: "FUNDING 进入极端带",
		fundingSignFlip: "FUNDING 符号翻转",
		fundingExitBand: "FUNDING 跌出极端带",
	}[ev.kind]
	leg := ev.row.HedgeCanonical
	if leg == "" {
		leg = "无"
	} else if ev.row.HedgeClass != "" {
		leg += " class=" + ev.row.HedgeClass
	}
	ann := func(v *float64) string {
		if v == nil {
			return "n/a"
		}
		return fmt.Sprintf("%+.1f", *v)
	}
	return fmt.Sprintf("[universe-radar] %s %s %s %s (leg=%s) ann %s%%→%s%%/yr vol=$%.0f",
		label, ev.row.Tier, ev.row.Venue, ev.row.Symbol, leg, ann(ev.oldAnn), ann(ev.row.FundingAnn), ev.row.Vol24h)
}

func alertLine(event string, n normRow) string {
	leg := n.HedgeCanonical
	if leg == "" {
		leg = "无"
	} else if n.HedgeClass != "" {
		leg += " class=" + n.HedgeClass
	}
	ann := "n/a"
	if n.FundingAnn != nil {
		ann = fmt.Sprintf("%.1f", *n.FundingAnn)
	}
	return fmt.Sprintf("[universe-radar] %s %s %s %s (leg=%s) ann=%s%%/yr vol=$%.0f",
		event, n.Tier, n.Venue, n.Symbol, leg, ann, n.Vol24h)
}

// sendRadarAlert 飞书批量推送（凭据/chat_id 缺失=log 跳过，不炸轮）。
func sendRadarAlert(ctx context.Context, st *pgstore.Store, lines []string) {
	appID := os.Getenv("ARB_FEISHU_APP_ID")
	appSecret := os.Getenv("ARB_FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Printf("universe-radar: 飞书凭据未配置，告警跳过（%d 条）", len(lines))
		return
	}
	fc := notify.NewFeishuClient(appID, appSecret)
	if !fc.Enabled() {
		log.Printf("universe-radar: 飞书客户端未启用，告警跳过（%d 条）", len(lines))
		return
	}
	chatID, err := st.FeishuChatID(ctx)
	if err != nil {
		log.Printf("universe-radar: 读 chat_id 失败: %v", err)
		return
	}
	if chatID == "" {
		log.Printf("universe-radar: chat_id 未绑定，告警跳过（%d 条）", len(lines))
		return
	}
	text := fmt.Sprintf("[universe-radar] %d 条告警\n", len(lines))
	for _, l := range lines {
		text += "• " + l + "\n"
	}
	if err := fc.SendText(ctx, chatID, text); err != nil {
		log.Printf("universe-radar: 飞书告警发送失败: %v", err)
		return
	}
	log.Printf("universe-radar: 飞书告警已推送（%d 条）", len(lines))
}
