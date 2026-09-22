// helpers.go —— D-376 S5：env/告警渲染/腿集 helper 组自 main.go 整组移出
// （同包纯搬移零行为变更；check-lines 超线修法，D-366 digest.go 先例）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
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

// radarStore D-389：radar 告警路径依赖的 store 面（*pgstore.Store 满足；测试可 fake）。
type radarStore interface {
	FeishuChatID(ctx context.Context) (string, error)
	TouchCexUniverseAlert(ctx context.Context, venue, symbol string, ts time.Time) error
	UpsertRadarWatchlist(ctx context.Context, symbol string, venues []string, reason string) error
	GetRadarDigestMembers(ctx context.Context) (map[string]map[string]pgstore.RadarDigestMember, error)
	ApplyRadarDigestRun(ctx context.Context, cur map[string][]string, pushed map[string][]string, now time.Time) error
}

// alertTouch 延后落库的边沿事件冷却戳（venue|symbol）。
type alertTouch struct{ venue, symbol string }

// realLegKey D-389 R0-A1：real_leg digest 集合 hash 键=pair 身份（不含费率——
// 费率漂移对已上纸面轨 pair 零新增行动项，不重推=设计取舍非缺陷）。
func realLegKey(p divPair) string { return p.Canon + " " + p.ShortVenue + "×" + p.LongVenue }

// pushDigestNewMembers D-392 S3：digest 成员级边沿——只报新入成员（集合进出抖不再触发）。
// 可报谓词：m∈cur 且 m∉prevSet（in_prev_set=false/无行）且（last_pushed NULL 或 <now−24h flap TTL）。
// 新入非空 → 组行（含「另 N 在册」尾注）并记 pending[kind]=新入成员（推送成功后才落库，
// 见 pushAndBookkeep→ApplyRadarDigestRun）；stored nil=查询失败 fail-open 视为全新入。
func pushDigestNewMembers(stored map[string]pgstore.RadarDigestMember, pending map[string][]string, lines *[]string, kind string, curKeys []string, itemOf func(string) string, now time.Time) {
	var newKeys []string
	inPrev := 0
	for _, m := range curKeys {
		st, ok := stored[m]
		if ok && st.InPrevSet {
			inPrev++
			continue // 在册续验不重报
		}
		if ok && st.LastPushedAt != nil && now.Sub(*st.LastPushedAt) < 24*time.Hour {
			continue // flap：跌出 24h 内回归压
		}
		newKeys = append(newKeys, m)
	}
	if len(newKeys) == 0 {
		log.Printf("universe-radar: digest 无新入 kind=%s cur=%d", kind, len(curKeys))
		return
	}
	items := make([]string, len(newKeys))
	for i, m := range newKeys {
		items[i] = itemOf(m)
	}
	*lines = append(*lines, fmt.Sprintf("[universe-radar] %s 新入 %d：%s（另 %d 在册续验不重复）",
		kind, len(newKeys), strings.Join(items, ", "), inPrev))
	pending[kind] = newKeys
}

// pushAndBookkeep D-389 S3（R0-A2/R2/R3）：整批发送→成功后记账。
// 批失败语义（已声明取舍）：sendRadarAlert 一次发整批，批内混边沿事件行与 digest 行；
// 发送失败=整批丢失——digest hash 不落（下轮重试，对），边沿事件行亦丢但冷却戳不落
// （TouchCexUniverseAlert 移到发送成功后，防「信号已冷却却没推出」静默丢 24h 前置缺陷）。
// UpsertRadarDigest 失败=上抛 error 终止本轮（R0-A3/R2：写侧静默吞错+读侧 fail-open
// =永久重推，噪声换形态复发；上抛后不落 hash 下轮重试且错误可见）。
func pushAndBookkeep(ctx context.Context, st radarStore, send func(context.Context, []string) error, lines []string, cur map[string][]string, pending map[string][]string, touches []alertTouch, now time.Time) error {
	if err := send(ctx, lines); err != nil {
		log.Printf("universe-radar: 告警整批未发出（%d 条丢失，digest 成员态与冷却戳均不落，下轮重试）：%v", len(lines), err)
		return nil
	}
	for _, t := range touches {
		if err := st.TouchCexUniverseAlert(ctx, t.venue, t.symbol, now); err != nil {
			log.Printf("universe-radar: touch alert %s|%s: %v", t.venue, t.symbol, err)
		}
	}
	if err := st.ApplyRadarDigestRun(ctx, cur, pending, now); err != nil {
		return fmt.Errorf("apply digest run: %w", err)
	}
	return nil
}

// sendRadarAlert 飞书批量推送（凭据/chat_id 缺失/发送失败=返回 error，调用侧不落状态）。
func sendRadarAlert(ctx context.Context, st radarStore, lines []string) error {
	appID := os.Getenv("ARB_FEISHU_APP_ID")
	appSecret := os.Getenv("ARB_FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Printf("universe-radar: 飞书凭据未配置，告警跳过（%d 条）", len(lines))
		return fmt.Errorf("feishu credentials not configured")
	}
	fc := notify.NewFeishuClient(appID, appSecret)
	if !fc.Enabled() {
		log.Printf("universe-radar: 飞书客户端未启用，告警跳过（%d 条）", len(lines))
		return fmt.Errorf("feishu client disabled")
	}
	chatID, err := st.FeishuChatID(ctx)
	if err != nil {
		log.Printf("universe-radar: 读 chat_id 失败: %v", err)
		return err
	}
	if chatID == "" {
		log.Printf("universe-radar: chat_id 未绑定，告警跳过（%d 条）", len(lines))
		return fmt.Errorf("feishu chat_id not bound")
	}
	text := fmt.Sprintf("[universe-radar] %d 条告警\n", len(lines))
	for _, l := range lines {
		text += "• " + l + "\n"
	}
	if err := fc.SendText(ctx, chatID, text); err != nil {
		return fmt.Errorf("feishu send: %w", err)
	}
	log.Printf("universe-radar: 飞书告警已推送（%d 条）", len(lines))
	return nil
}
