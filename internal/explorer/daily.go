// daily.go 日扫五项装配（D-513 设计稿 §2）：五段确定性数字拼一次主调用 prompt；
// LLM 输出 strict protojson 提案块 → 行级校验 → explorer_proposals + 收件箱行。
// 输出行：[explorer] daily ok: proposals=N drop=M。
package explorer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"arb/internal/analyst"
	"arb/internal/inboxfmt"
	"arb/internal/store/pgstore"
)

// dailySystemPrompt 探矿员系统约束（壳内固定，配置随码；红线进 prompt——
// 不预测价格/不给仓位建议/不跨场所借数据/数字只引用输入段不得自算）。
const dailySystemPrompt = `你是 ARB 套利系统的探矿员（只读研究员）。输入=五段确定性数字（Go 计算，禁止改动/重算/自算）。
输出（严格两段）：
1. 叙事摘要 ≤300 字；
2. 一个 fenced 块 ` + "```explorer-proposals" + ` 内含 JSON：{"narrative":"...","proposals":[{"canonical":"...","best_pair":"venue_a×venue_b","direction":"≤16字","rationale":"一行","anchor":"输入段字段名","evidence":{}}]}。
红线：不预测价格；不给仓位建议；不跨场所借数据；数字只能引用输入段原值。
提案 ≤10 条；canonical 必须来自输入段出现过的键；anchor 必须等于所据数字所在段名。`

// stateKeyWatchlist explorer_state 快照键（②新符号/上市 diff 源）。
const stateKeyWatchlist = "watchlist_snapshot"

// watchlistSnapshot ②昨快照形态：radar watchlist 符号集 + broker_symbol_map 计数。
type watchlistSnapshot struct {
	Symbols  []string `json:"symbols"`
	MapCount int      `json:"map_count"`
}

// DailyResult 日扫产出（测试断言面）。
type DailyResult struct {
	ScanDate   time.Time
	Proposals  int      // 实际落库行数
	Drop       int      // 行级丢弃计数（非法行+批违例）
	NewSymbols []string // ②diff 新增符号
	Narrative  string   // LLM 叙事摘要（≤300字，日志/收件箱用）
}

// RunDaily 执行日扫五项。同日重跑幂等（dedupe 索引挡提案重复；state 覆盖幂等）。
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

	// ③ 新增被拒候选（昨日起 status=denied 行清单——仅清单非重放，重放=CP3 重件）。
	bjNow := now.In(BJTime)
	dayStart := time.Date(bjNow.Year(), bjNow.Month(), bjNow.Day(), 0, 0, 0, 0, BJTime).AddDate(0, 0, -1)
	var denied []pgstore.PerpFundingAdmissionRow
	adm, err := deps.Store.ListPerpFundingAdmission(ctx)
	if err != nil {
		return nil, fmt.Errorf("list admission: %w", err)
	}
	for _, r := range adm {
		if r.Status == "denied" && !r.UpdatedAt.Before(dayStart) {
			denied = append(denied, r)
		}
	}

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

	// 已知 canonical 集=①canonical ∪ ②watchlist ∪ ③admission canonical（校验锚）。
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
	for _, r := range adm {
		known[r.Canonical] = true
	}

	prompt := buildDailyPrompt(res.ScanDate, disp, newSyms, denied, events, flagged)
	if !deps.Rate.Allow() {
		return res, fmt.Errorf("explorer: rate limited")
	}
	var aiText strings.Builder
	_, err = deps.Run(ctx, deps.Client, deps.Tools, explorerToolSpecs(), dailySystemPrompt,
		[]analyst.Message{analyst.TextContent("user", prompt)},
		analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
	if err != nil {
		return res, fmt.Errorf("analyst run: %w", err)
	}

	batch, ok := parseBatch(aiText.String())
	if !ok {
		// 截断/拒收/超限=整批零落库（A6 截断用例锚面）；叙事不受影响仍记日志级。
		res.Drop = maxBatchProposals + 1
		return res, nil
	}
	rows, drop := validateProposals(batch, known)
	res.Drop = drop
	res.Narrative = extractNarrative(batch)
	for i := range rows {
		rows[i].ScanDate = res.ScanDate
		rows[i].ScanKind = "daily"
	}
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

// buildDailyPrompt 五段数字拼装（字段名=anchor 合法集）。
func buildDailyPrompt(day time.Time, disp []pgstore.ExplorerDispersion,
	newSyms []string, denied []pgstore.PerpFundingAdmissionRow,
	events []string, flagged []pgstore.ExplorerIdlePool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "scan_date=%s（北京历日）。五段确定性输入如下：\n\n", day.Format("2006-01-02"))
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
	b.WriteString("\n## denied_admissions（canonical/best_pair/note）\n")
	for _, r := range denied {
		fmt.Fprintf(&b, "%s %s note=%s\n", r.Canonical, r.BestPair, inboxfmt.CleanCell(r.Note))
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
