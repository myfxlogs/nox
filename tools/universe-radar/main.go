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
	"path/filepath"
	"strings"
	"time"

	"arb/internal/httpx" // SSOT：自动注入 CA + X-Proxy-Token
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
	cacheDir, cerr := os.UserCacheDir()
	if cerr != nil {
		cacheDir = os.TempDir()
	}
	for _, src := range allSources(hc, proxy, st.Pool(), filepath.Join(cacheDir, "arb-universe-radar")) {
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

	// FUNDING_DIVERGENCE：D-376 告警分层——双腿确证 sim → 既有 event+cooldown+alert 路径零变更；
	// 任一侧真/未知 → digest 行+纸面（不走告警 event）；suspect（极端 funding 病态尾部）→ digest
	// 计数不开仓。perp×perp 纸面步进吃全部非 suspect 对（24h 冷却只挡告警不挡纸面）。
	divSeen := make(map[string]bool)
	pairs, classFiltered := divergences(rows, divPct, volMinUSD)
	simMap, err := st.ListVenueIsReal(ctx) // venueKey→*bool；nil=未知保守当真腿
	if err != nil {
		log.Printf("universe-radar: list venue is_real: %v（保守全当真腿，digest 化）", err)
		simMap = map[string]*bool{}
	}
	simPairs, realPairs, suspectList := splitDivPairs(pairs, simMap)
	for _, p := range simPairs {
		e := divEvent(p)
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
	// D-376 B 面：perp×perp 纸面轨步进（radar 1h 轮，streak=2≈2h；见 paper_perp.go 头注）。
	nonSuspect := append(append([]divPair{}, simPairs...), realPairs...)
	pOpened, pExited, perr := stepPaperPerpCarry(ctx, st, nonSuspect, rows, divPct, now)
	if perr != nil {
		log.Printf("universe-radar: paper perp step: %v", perr)
	} else {
		if len(pOpened) > 0 {
			if aerr := st.InsertAlert(ctx, nil, "info", "[perp-perp 纸面开仓] "+strings.Join(pOpened, ", ")); aerr != nil {
				log.Printf("universe-radar: paper perp alert: %v", aerr)
			}
		}
		if len(pOpened) > 0 || pExited > 0 {
			log.Printf("[universe-radar] paper perp carry: opened=%d exited=%d", len(pOpened), pExited)
		}
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
			LastBasisBps: basisBps(n.Mark, n.Index), // D-354 S1：基差落库（okx 无面→NULL）
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
		// D-392：digest 成员级边沿状态读出——查询失败=fail-open（视为全新入照推，
		// digest 是低价值行宁滥勿漏；代价=R0-A4 已声明：DB 故障期间 digest
		// 可能每轮重推=已接受取舍，触发源从集合不变变 DB 故障）。
		stored, derr := st.GetRadarDigestMembers(ctx)
		if derr != nil {
			log.Printf("universe-radar: GetRadarDigestMembers 失败，fail-open 照推: %v", derr)
			stored = nil
		}
		// cur 收全部四类键含空集（S2：in_prev 清零不得依赖该类本轮有成员，
		// 否则空集周期残留标记永久误压「离开>24h 再回归」）。
		cur := map[string][]string{"legless_band": nil, "class_filtered": nil, "real_leg": nil, "suspect": nil}
		pending := map[string][]string{}
		var touches []alertTouch
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
			// D-389 R0-A2：冷却戳移到发送成功后（发送失败=信号冷却却未推出=静默丢 24h 前置缺陷收编）。
			touches = append(touches, alertTouch{e.Venue, e.Symbol})
		}
		// D-353 S1：事件式——只在状态转移（新进/翻转/跌出）时发声，稳态零报；
		// last_alert_ts 24h 冷却保留作 flap 抑制（进出带抖动至多一报/日）。
		// D-369 S1/S2（R3 拆分）：digest 累加/分流判定/watchlist 写侧 → digest.go
		// d369RouteFundingEvents（块体逐字节搬移零行为变更）。
		d369RouteFundingEvents(ctx, st, rows, old, events, now, fundingMinPct, volMinUSD, &lines, stored, cur, pending, &touches)
		// D-374 A 面：非币类 canonical divergence 压 digest（class_filtered，成员级边沿）。
		cur["class_filtered"] = classFiltered
		pushDigestNewMembers(stored["class_filtered"], pending, &lines, "class_filtered", classFiltered,
			func(m string) string { return m }, now)
		// D-376 S3：真腿 divergence digest 行+极端 funding suspect 压制行（同款汇总风格；
		// 真腿不做完全静默——digest 保留可见性，行动项=纸面验证轨）。
		// D-389 R0-A1 裁定选项 (a)：real_leg 集合 hash 键=pair 身份（canon+short×long），
		// 不含费率——对已上纸面验证轨的 pair，费率漂移零新增行动项；代价=漂移不再
		// 重推（行内 @x.x% 数字变化而 pair 不变时不推）=设计取舍非缺陷。
		// 成员键=pair 身份（canon+short×long），item 含费率（新入成员信息量保留）。
		realItem := map[string]string{}
		for _, p := range realPairs {
			cur["real_leg"] = append(cur["real_leg"], realLegKey(p))
			realItem[realLegKey(p)] = fmt.Sprintf("%s %s×%s@%+.1f%%/%+.1f%%", p.Canon, p.ShortVenue, p.LongVenue, p.FundShort, p.FundLong)
		}
		pushDigestNewMembers(stored["real_leg"], pending, &lines, "real_leg", cur["real_leg"],
			func(m string) string { return realItem[m] }, now)
		cur["suspect"] = suspectList
		pushDigestNewMembers(stored["suspect"], pending, &lines, "suspect", suspectList,
			func(m string) string { return m }, now)
		if len(lines) > 0 {
			// D-389 R0-A3/R2 + D-392 S5：ApplyRadarDigestRun 失败=上抛终止本轮
			// （不落成员态下轮重试，错误可见——读侧 fail-open + 写侧静默吞错=永久重推）。
			if err := pushAndBookkeep(ctx, st, func(c context.Context, l []string) error {
				return sendRadarAlert(c, st, l)
			}, lines, cur, pending, touches, now); err != nil {
				return err
			}
		} else {
			// 零行轮次仍记账：in_prev 清零+过期清理每轮必跑（S2 缺陷 2——
			// 静默轮不记账则跌出成员 in_prev 残留，>24h 回归被永久误压）。
			// pending 必为空（无行即无新推），last_pushed_at 不动。
			if err := st.ApplyRadarDigestRun(ctx, cur, nil, now); err != nil {
				log.Printf("universe-radar: digest 成员态记账失败（零行轮，下轮重试）: %v", err)
			}
		}
	}

	if err := st.PruneCexRadar(ctx, 90); err != nil {
		return fmt.Errorf("prune radar: %w", err)
	}
	return firstErr
}
