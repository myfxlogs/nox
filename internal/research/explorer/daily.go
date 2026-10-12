// daily.go 日扫五项装配（D-513 设计稿 §2）：四段确定性数字拼一次主调用 prompt；
// LLM 输出 strict protojson 提案块 → 行级校验 → explorer_proposals + 收件箱行。
// 输出行：[explorer] daily ok: proposals=N drop=M。
package explorer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"arb/internal/ir/inboxfmt"
	"arb/internal/platform/analyst"
	"arb/internal/store/pgstore"
)

// dailySystemPrompt 探矿员系统约束（壳内固定，配置随码；红线进 prompt——
// 不预测价格/不给仓位建议/不跨场所借数据/数字只引用输入段不得自算）。
const dailySystemPrompt = `你是 ARB 套利系统的探矿员（只读研究员）。输入=四段确定性数字（Go 计算，禁止改动/重算/自算）。
输出（严格两段）：
1. 叙事摘要 ≤300 字；
2. 一个 fenced 块 ` + "```explorer-proposals" + ` 内含 JSON：{"narrative":"...","proposals":[{"canonical":"...","best_pair":"venue_a×venue_b","direction":"≤16字","rationale":"一行","anchor":"输入段字段名","evidence":{}}],"probe_proposals":[{...}]}。
红线：不预测价格；不给仓位建议；不跨场所借数据；数字只能引用输入段原值。
工作契约：每条结论性判断附一行证伪条件「若 <具体可观测值> 则此判断不成立」，说不出的降级为假设陈述；
任何「需观察/后续跟踪」必须写明触发点（何时/何阈值/触发什么动作），无触发点的等待句不写；
引用数字附口径说明（输入段名/时间窗/过滤语义），先自证语义再引用；
两个数字/状态互斥时立即停推——把矛盾升为最高优先级调查对象，复验两边来源（时点差/口径差/层差），禁调和性解释或择一采用；
结论分层归因注明所属层（采集/存储/计算/展示），跨层断言须逐层给证据，禁跨层跳跃归因。
提案 ≤10 条；canonical 必须来自输入段出现过的键；anchor 必须等于所据数字所在段名。
canonical 必须是交易品种符号（如 ONEUSDT/XAUUSD）；账户/资金池描述（如 "idle 3001157"）不是 canonical——填了会被行级校验整行拒收。
probe_proposals 可省略——仅当某假设需小额单腿实测证伪时给出，契约全字段必填：
{"purpose":"目的","falsify":"可量化证伪标准（须含数字阈值）","venue_key":"venue:type","canonical":"输入段出现过的键","side":"BUY|SELL","qty":数值>0,"notional_usd":数值>0且≤配置键ARB_PROBE_MAX_NOTIONAL,"expected_cost_usd":数值≥0,"auto_close_at":"RFC3339 未来时刻","basis":"数据依据（人读散文，机审不解析）","premises":[{"kind":"term_dispersion|idle_funds","canonical":"品种键（term_dispersion 必填）","venue":"裸 venue 名（idle_funds 必填）","free_usd":数值（idle_funds 可填）"}]}。缺一即废该行；premises 是机审唯一判据，禁把前提写进 basis 散文冒充。`

// stateKeyWatchlist explorer_state 快照键（②新符号/上市 diff 源）。
const stateKeyWatchlist = "watchlist_snapshot"

// watchlistSnapshot ②昨快照形态：radar watchlist 符号集 + broker_symbol_map 计数。
type watchlistSnapshot struct {
	Symbols  []string `json:"symbols"`
	MapCount int      `json:"map_count"`
}

// DailyResult 日扫产出（测试断言面）。
type DailyResult struct {
	ScanDate      time.Time
	Proposals     int      // 实际落库行数（scan_kind='daily'）
	Drop          int      // 行级丢弃计数（非法行+批违例+探针契约拒行）
	NewSymbols    []string // ②diff 新增符号
	Narrative     string   // LLM 叙事摘要（≤300字，日志/收件箱用）
	ProbeInserted int      // D-048①：探针提案落库行数（scan_kind='probe'）
}

// RunDaily 执行日扫五项。同日重跑幂等（dedupe 索引挡提案重复；state 覆盖幂等）。
// 每跑必 log「候选数/LLM 调用结果/解析结果/入库数」（0 提案归因面）；
// LLM 调用失败/解析失败落 alerts warn 不吞。
func RunDaily(ctx context.Context, deps Deps) (*DailyResult, error) {
	deps.fill()
	if deps.Store == nil {
		return nil, fmt.Errorf("explorer: store 未配置")
	}
	if deps.Client == nil {
		return nil, fmt.Errorf("explorer: client 未配置")
	}
	now := deps.Now()
	res := &DailyResult{ScanDate: ScanDate(now)}
	// D-048④ 观测面：llmRes=skip→ok|err；parseRes=skip→ok|fail；candidates=LLM 产出行数。
	llmRes, parseRes := "skip", "skip"
	candidates := 0
	defer func() {
		log.Printf("[explorer] daily run: candidates=%d llm=%s parse=%s inserted=%d probe_inserted=%d drop=%d",
			candidates, llmRes, parseRes, res.Proposals, res.ProbeInserted, res.Drop)
	}()

	// ① 期限库异常（跨 venue 离散度+7d 差——Go 已算，LLM 只标值得提案的）。
	disp, err := deps.Store.ExplorerTermDispersion(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("term dispersion: %w", err)
	}
	sort.Slice(disp, func(i, j int) bool { return disp[i].Range > disp[j].Range })
	if len(disp) > 30 {
		disp = disp[:30]
	}

	// ② 新符号/上市（explorer_state 昨快照 diff；快照当日落新值，幂等覆盖）。
	newSyms, snapErr := d513DiffSymbols(ctx, deps, now)
	if snapErr != nil {
		return nil, fmt.Errorf("symbol snapshot: %w", snapErr)
	}
	res.NewSymbols = newSyms

	// ④ 公告流要点（近 24h [事件] 前缀 alerts 原文）。
	var events []string
	alerts, err := deps.Store.ListAlerts(ctx, 200, 0)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	for _, a := range alerts {
		if a.Ts.After(now.Add(-24*time.Hour)) && strings.HasPrefix(strings.TrimSpace(a.Message), "[事件]") {
			events = append(events, a.Message)
		}
		if len(events) >= 30 {
			break
		}
	}

	// ⑤ 资金不眠异常（闲置/破地板池，Go 直查——v1.2 R1：不读 digest markdown 当 API）。
	idles, err := deps.Store.ExplorerIdleFunds(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("idle funds: %w", err)
	}
	var flagged []pgstore.ExplorerIdlePool
	for _, p := range idles {
		if p.Idle || p.BelowFloor {
			flagged = append(flagged, p)
		}
	}

	// 已知 canonical 集=①canonical ∪ ②watchlist（校验锚）。
	known := map[string]bool{}
	for _, d := range disp {
		known[d.Canonical] = true
	}
	for _, s2 := range newSyms {
		known[s2] = true
	}
	wl, _ := deps.Store.LoadRadarWatchlist(ctx)
	for _, w := range wl {
		known[w.Symbol] = true
	}

	prompt := buildDailyPrompt(res.ScanDate, disp, newSyms, events, flagged)
	runOnce := func() (analyst.LoopResult, string, error) {
		var aiText strings.Builder
		lr, err := deps.Run(ctx, deps.Client, deps.Tools, explorerToolSpecs(), dailySystemPrompt,
			[]analyst.Message{analyst.TextContent("user", prompt)},
			analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
		return lr, aiText.String(), err
	}
	if !deps.Rate.Allow() {
		return res, fmt.Errorf("explorer: rate limited")
	}
	lr, aiText, err := runOnce()
	if err != nil {
		llmRes = "err"
		deps.alert(ctx, "warn", "[探索] LLM 调用失败："+err.Error()) // D-048④：不得吞
		return res, fmt.Errorf("analyst run: %w", err)
	}
	llmRes = "ok"

	// parse fail → 同 prompt 补救一轮（上限两轮，Rate.Allow 每轮过闸不过即停）；
	// StopReason 随轮次刷新供分型告警（symdig D-530/D-589 同型先例）。
	batch, truncDrop, ok, kind := parseBatchDiag(aiText)
	stop := lr.StopReason
	if !ok {
		if deps.Rate.Allow() {
			log.Printf("[explorer] 提案块第1轮解析失败（kind=%s stop=%s）——同 prompt 进第2轮补救", kind, stop)
			lr, aiText, err = runOnce()
			if err != nil {
				llmRes = "err"
				deps.alert(ctx, "warn", "[探索] 提案块补救轮 LLM 调用失败："+err.Error())
				return res, fmt.Errorf("analyst run: %w", err)
			}
			stop = lr.StopReason
			batch, truncDrop, ok, kind = parseBatchDiag(aiText)
			if ok {
				log.Printf("[explorer] 第2轮补救成功")
			}
		} else {
			log.Printf("[explorer] 提案块解析失败（kind=%s）但限流过闸拒——跳过第2轮补救", kind)
		}
	}
	if !ok {
		// 真截断（未闭合 fenced）/protojson 拒收=整批零落库（A6 截断用例锚面）；
		// 批超限良性违例已改截断留前 10（D-078），不再走此整批拒分支。叙事不受影响仍记日志级。
		parseRes = "fail"
		res.Drop = maxBatchProposals + 1
		level, why := "warn", kind
		switch {
		case stop == "max_tokens":
			level, why = "error", "max_tokens 截断（输出超上限，fence 未闭合/JSON 不全）"
		case kind == "no-fence":
			why = "no-fence（输出缺/未闭合 explorer-proposals 块）"
		default:
			why = "unmarshal（protojson 拒收，sanitize 已试）"
		}
		deps.alert(ctx, level, "[探索] 提案块解析失败（"+why+"——两轮后整批零落库）") // D-048④：不得吞
		return res, nil
	}
	parseRes = "ok"
	candidates = len(batch.GetProposals()) + len(batch.GetProbeProposals())
	rows, drop := validateProposals(batch, known)
	res.Drop = drop + truncDrop // D-078：批超限截断条数并入 drop（计数保真）
	res.Narrative = extractNarrative(batch)
	for i := range rows {
		rows[i].ScanDate = res.ScanDate
		rows[i].ScanKind = "daily"
	}
	// 探针提案契约校验→落库→「待审批探针」推送（缺字段拒行+log）。
	probeInserted, probeDrop := runProbeProposals(ctx, deps, batch, known, now, res.ScanDate)
	res.ProbeInserted = probeInserted
	res.Drop += probeDrop
	n, err := deps.Store.ExplorerProposalInsert(ctx, rows)
	if err != nil {
		return res, err
	}
	res.Proposals = n
	if len(rows) > 0 {
		var ib []inboxfmt.Row
		for _, r := range rows {
			ib = append(ib, inboxfmt.Row{Source: inboxfmt.SrcExplorer, Title: r.Canonical,
				Reason: r.Rationale, Action: r.Direction + " " + r.BestPair})
		}
		if err := inboxfmt.Append(deps.InboxPath, res.ScanDate.Format("2006-01-02"), ib); err != nil {
			deps.alert(ctx, "warn", "[探索] 收件箱追加失败："+err.Error())
		}
	}
	return res, nil
}

// d513DiffSymbols ②快照 diff：昨快照（explorer_state）vs 当前 watchlist 符号集。
// 快照缺失（首跑）→ 全新=零 diff 噪声不告警形态：返回 nil 新增但仍写快照。
func d513DiffSymbols(ctx context.Context, deps Deps, now time.Time) ([]string, error) {
	wl, err := deps.Store.LoadRadarWatchlist(ctx)
	if err != nil {
		return nil, err
	}
	maps, err := deps.Store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, err
	}
	cur := watchlistSnapshot{MapCount: len(maps)}
	seen := map[string]bool{}
	for _, w := range wl {
		if !seen[w.Symbol] {
			seen[w.Symbol] = true
			cur.Symbols = append(cur.Symbols, w.Symbol)
		}
	}
	sort.Strings(cur.Symbols)
	var added []string
	if raw, ok, err := deps.Store.ExplorerStateGet(ctx, stateKeyWatchlist); err != nil {
		return nil, err
	} else if ok {
		var prev watchlistSnapshot
		if json.Unmarshal(raw, &prev) == nil {
			prevSet := map[string]bool{}
			for _, s2 := range prev.Symbols {
				prevSet[s2] = true
			}
			for _, s2 := range cur.Symbols {
				if !prevSet[s2] {
					added = append(added, s2)
				}
			}
		}
	}
	raw, _ := json.Marshal(cur)
	if err := deps.Store.ExplorerStatePut(ctx, stateKeyWatchlist, raw); err != nil {
		return nil, err
	}
	return added, nil
}

// buildDailyPrompt 四段数字拼装（字段名=anchor 合法集；D-057 去 denied 段）。
func buildDailyPrompt(day time.Time, disp []pgstore.ExplorerDispersion,
	newSyms []string,
	events []string, flagged []pgstore.ExplorerIdlePool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "scan_date=%s（北京历日）。四段确定性输入如下：\n\n", day.Format("2006-01-02"))
	b.WriteString("## term_dispersion（kind/canonical/venues/min/max/range/mean/stddev/prev_mean/delta/min_venue/max_venue）\n")
	for _, d := range disp {
		fmt.Fprintf(&b, "%s %s venues=%d min=%.6g max=%.6g range=%.6g mean=%.6g stddev=%.6g",
			d.Kind, d.Canonical, d.Venues, d.Min, d.Max, d.Range, d.Mean, d.StdDev)
		if d.HasPrev {
			fmt.Fprintf(&b, " prev_mean=%.6g delta=%.6g", d.PrevMean, d.Delta)
		}
		fmt.Fprintf(&b, " [%s..%s]\n", d.MinVenue, d.MaxVenue)
	}
	b.WriteString("\n## new_symbols\n")
	for _, s2 := range newSyms {
		b.WriteString(s2 + "\n")
	}
	b.WriteString("\n## announcements_24h\n")
	for _, e := range events {
		b.WriteString(inboxfmt.CleanCell(e) + "\n")
	}
	b.WriteString("\n## idle_funds（venue/venue_type/balance/free_margin/income30d/idle/below_floor）\n")
	for _, p := range flagged {
		fmt.Fprintf(&b, "%s %s balance=%.4g free=%.4g income30d=%.4g idle=%v below_floor=%v\n",
			p.Venue, p.VenueType, p.Balance, p.FreeMargin, p.Income30dUSD, p.Idle, p.BelowFloor)
	}
	return b.String()
}
