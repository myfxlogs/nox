// D-307 CEX 全量合约雷达（独立 cron 工具，参照 tools/xasset-monitor 模式）。
//
// 每轮：三所全量 perp 枚举+funding/量快照（9 个 HTTP 调用，全部经 cex-proxy 公共端点无凭据）
// → 归一+对冲腿 join+tier 分诊 → universe diff 事件 → fin_cex_universe upsert +
// fin_cex_events + fin_cex_radar 全量落库（L0 全宇宙观测）→ 过线者 Feishu 告警
// → radar 90 天滚动自净。
//
// 只发现只报告：不改 cexSymbols/asset_equiv/任何配置；L1+ 晋级走决策。
// 首轮 bootstrap 静默：旧 universe 为空 → 只落库零事件零告警（防 ~2200 NEW 洪泛）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"arb/internal/httpx" // SSOT：自动注入 CA + X-Proxy-Token
	"arb/internal/notify"
	"arb/internal/store/pgstore"
)

func main() {
	ctx := context.Background()
	if err := run(ctx); err != nil {
		log.Fatalf("universe-radar: %v", err)
	}
}

func run(ctx context.Context) error {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		return fmt.Errorf("DB_DSN 未设置")
	}
	proxy := os.Getenv("BINANCE_PROXY_URL")
	if proxy == "" {
		return fmt.Errorf("BINANCE_PROXY_URL 未设置")
	}
	fundingMinPct := envFloat("RADAR_FUNDING_MIN_PCT", 15)
	volMinUSD := envFloat("RADAR_VOL_MIN_USD", 5e6)
	divPct := envFloat("RADAR_DIV_PCT", 20)

	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect pg: %w", err)
	}
	defer st.Close()
	hc := httpx.Client()
	now := time.Now()

	// 对冲腿集：broker_symbol_map distinct canonical + asset_equiv enabled canonical
	//（classOf 附带 asset_equiv canonical→asset_class，告警行展示用）。
	hedgeSet, classOf, err := hedgeCanonicals(ctx, st)
	if err != nil {
		return fmt.Errorf("hedge set: %w", err)
	}

	// 扫三所：单所失败 fail-open（log+继续，返回 error 让 cron 日志可见）。
	var rows []normRow
	var firstErr error
	for _, src := range allSources(hc, proxy) {
		rs, err := src.Scan(ctx)
		if err != nil {
			log.Printf("universe-radar: venue=%s 扫描失败: %v", src.Venue(), err)
			if firstErr == nil {
				firstErr = fmt.Errorf("venue=%s: %w", src.Venue(), err)
			}
			continue
		}
		rows = append(rows, normalize(src.Venue(), rs, hedgeSet, classOf)...)
	}

	old, err := st.ListCexUniverse(ctx)
	if err != nil {
		return fmt.Errorf("list universe: %w", err)
	}
	bootstrap := len(old) == 0

	events := diffUniverse(old, rows)

	// FUNDING_DIVERGENCE：冷却 ≥24h 才落事件+告警（冷却内零写入）。
	divSeen := make(map[string]bool)
	for _, e := range divergences(rows, divPct, volMinUSD) {
		if bootstrap || divSeen[e.Symbol] {
			continue
		}
		ts, ok, err := st.LastCexEventTS(ctx, "FUNDING_DIVERGENCE", e.Symbol)
		if err != nil {
			return fmt.Errorf("divergence cooldown %s: %w", e.Symbol, err)
		}
		if ok && now.Sub(ts) < 24*time.Hour {
			continue
		}
		divSeen[e.Symbol] = true
		events = append(events, e)
	}

	// 落库：universe upsert + events + radar 全量行（L0）。
	urows := make([]pgstore.CexUniverseRow, 0, len(rows))
	rrows := make([]pgstore.CexRadarRow, 0, len(rows))
	byKey := make(map[string]normRow, len(rows))
	for _, n := range rows {
		byKey[n.Venue+"|"+n.Symbol] = n
		urows = append(urows, pgstore.CexUniverseRow{
			Venue: n.Venue, Symbol: n.Symbol, Base: normBase(n.Symbol), Class: n.Class,
			Status: n.Status, LaunchTS: launchPtr(n.LaunchTS),
			HedgeCanonical: n.HedgeCanonical, Tier: n.Tier,
			FundingAnn: n.FundingAnn, Vol24h: &n.Vol24h, CtVal: n.CtVal,
		})
		rrows = append(rrows, pgstore.CexRadarRow{
			Venue: n.Venue, Symbol: n.Symbol, FundingAnn: n.FundingAnn,
			Vol24h: &n.Vol24h, Mark: &n.Mark, Index: &n.Index,
			HedgeCanonical: n.HedgeCanonical, Tier: n.Tier,
		})
	}
	if err := st.UpsertCexUniverse(ctx, urows, now); err != nil {
		return fmt.Errorf("upsert universe: %w", err)
	}
	if err := st.InsertCexEvents(ctx, events); err != nil {
		return fmt.Errorf("insert events: %w", err)
	}
	if err := st.InsertCexRadar(ctx, rrows, now); err != nil {
		return fmt.Errorf("insert radar: %w", err)
	}
	log.Printf("universe-radar: 扫描 %d 行，事件 %d 条%s", len(rows), len(events),
		map[bool]string{true: "（bootstrap 静默）", false: ""}[bootstrap])

	// 告警（bootstrap 零告警）。last_alert_ts 同 symbol 24h 冷却对转移事件与
	// funding 类统一生效（FUNDING_DIVERGENCE 走自身事件表冷却，venue='multi' 无 universe 行）。
	var lines []string
	if !bootstrap {
		for _, e := range events {
			if e.EventType == "FUNDING_DIVERGENCE" {
				lines = append(lines, fmt.Sprintf("[universe-radar] FUNDING_DIVERGENCE %s %s", e.Symbol, e.Detail))
				continue
			}
			key := e.Venue + "|" + e.Symbol
			if o, ok := old[key]; ok && !cooldownOK(o.LastAlertTS, now) {
				continue // 冷却内：事件照落库，告警不重发
			}
			lines = append(lines, alertLine(e.EventType, byKey[key]))
			if err := st.TouchCexUniverseAlert(ctx, e.Venue, e.Symbol, now); err != nil {
				log.Printf("universe-radar: touch alert %s: %v", key, err)
			}
		}
		// D-353 S1：事件式——只在状态转移（新进/翻转/跌出）时发声，稳态零报；
		// last_alert_ts 24h 冷却保留作 flap 抑制（进出带抖动至多一报/日）。
		for _, ev := range fundingTransitions(rows, old, fundingMinPct, volMinUSD) {
			if o, ok := old[ev.row.Venue+"|"+ev.row.Symbol]; ok && !cooldownOK(o.LastAlertTS, now) {
				continue
			}
			lines = append(lines, fundingEventLine(ev))
			if err := st.TouchCexUniverseAlert(ctx, ev.row.Venue, ev.row.Symbol, now); err != nil {
				log.Printf("universe-radar: touch alert %s|%s: %v", ev.row.Venue, ev.row.Symbol, err)
			}
		}
		if len(lines) > 0 {
			sendRadarAlert(ctx, st, lines)
		}
	}

	if err := st.PruneCexRadar(ctx, 90); err != nil {
		return fmt.Errorf("prune radar: %w", err)
	}
	return firstErr
}

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
