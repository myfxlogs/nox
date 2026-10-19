// llm.go D-464 S3：LLM 深挖——对 unpaired/unmapped/mispaired 行做语义别名提案。
// 输出只进收件箱文件（AI 只读纪律同 D-463：不落 alias 表不直改 map）。
package symdig

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"time"

	"nox/internal/ir/inboxfmt"
	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// maxLLMRows 单轮深挖行数上限（防 token 爆）。
const maxLLMRows = 30

// symdigLLMBatch 分批调用粒度（D-630 系提案噪面同族修——批内输出短天然
// 不撞 max_tokens；30 picks → 3 批×10 行逐次调替代两轮续提上限）。
const symdigLLMBatch = 10

const symdigSystemPrompt = `你是 NOX 系统的 symbol 配对深挖员。用户消息给出 MT 经纪商未配对/疑似错配品种清单（JSON）。
任务：对每个品种判断是否存在语义别名可配对（例：GOLD→XAU、GER40→DE30、PLATINUM→XPT、WS30→US30、NIKKEI↔JP225）或明显的映射漂移。
只输出一个 ` + "```symdig-proposals" + ` fenced JSON 数组，每项 {"symbol","venue","proposed_canonical","evidence","confidence"}；
evidence 必须基于给定字段（品种名/canonical/venue），≤80 字符，禁止纯猜测编造。无提案输出空数组。块外文字不解析。`

// proposalsRe/开放块容忍（EOF 截断容忍，同 sysretro openFenceRe 先例）。
var (
	proposalsRe = regexp.MustCompile("(?s)```symdig-proposals\\s*\n(.*?)```")
	openPropRe  = regexp.MustCompile("(?s)```symdig-proposals\\s*\n(.*)$")
)

// proposal LLM 提案行。
type proposal struct {
	Symbol            string  `json:"symbol"`
	Venue             string  `json:"venue"`
	ProposedCanonical string  `json:"proposed_canonical"`
	Evidence          string  `json:"evidence"`
	Confidence        float64 `json:"confidence"`
}

// llmDig 深挖一轮：findings 精选 → AI 提案 → 收件箱（只读，不改库）。
func llmDig(ctx context.Context, deps Deps, sum *Summary, runTS time.Time) error {
	// 盲区修：收件箱是持久面——已有 pending 提案的 symbol 跨日去重不进 picks
	//（head 品种不再每轮烧 token 重复提案，tail 品种有额度被覆盖）。
	pending := pendingInboxSymbols(deps.InboxPath)
	var picks []pgstore.SymdigFinding
	for _, r := range sum.Rows {
		if r.Klass == KlassUnpaired || r.Klass == KlassUnmappedUnknown || r.Klass == KlassMispaired {
			if pending[r.Symbol] {
				continue
			}
			picks = append(picks, r)
			if len(picks) >= maxLLMRows {
				break
			}
		}
	}
	if len(picks) == 0 {
		return nil
	}
	// 分批调用（替代两轮续提）：findings 分小批逐次调——批内输出短天然不撞
	// max_tokens（原「30 行单轮+malformed 剩集再调」两轮上限病灶根治）；每批
	// 独立解析合并提案，malformed 批不阻断后续批（降级面如实告警）。
	var all []proposal
	covered := map[string]bool{}
	var lastMalformed bool
	var lastStop string
	var anyTruncMalformed bool // ③荐位：任一批 max_tokens+malformed 累计——
	// 票面原意「任一批截断→error」（原 lastStop 仅最后 malformed 批=混合序降级 warn 病灶）
	for bi := 0; bi < len(picks); bi += symdigLLMBatch {
		end := bi + symdigLLMBatch
		if end > len(picks) {
			end = len(picks)
		}
		batch := picks[bi:end]
		payload, _ := json.Marshal(batch)
		var aiText strings.Builder
		res, err := deps.Run(ctx, deps.Client, nil, nil, symdigSystemPrompt,
			[]analyst.Message{analyst.TextContent("user", "未配对/错配品种清单：\n```json\n"+string(payload)+"\n```")},
			analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
		if err != nil {
			return fmt.Errorf("analyst run batch %d: %w", bi/symdigLLMBatch+1, err)
		}
		props, malformed := parseProposals(aiText.String())
		all = append(all, props...)
		for _, p := range props {
			covered[p.Symbol] = true
		}
		if malformed {
			lastMalformed, lastStop = malformed, res.StopReason
			if res.StopReason == "max_tokens" {
				anyTruncMalformed = true
			}
			log.Printf("[symdig] 批 %d 输出异常（%s）——%d/%d 品种提案落空（续批照跑）",
				bi/symdigLLMBatch+1, res.StopReason, len(batch)-countCovered(props, batch), len(batch))
		}
	}
	// 结案：任一批 max_tokens+malformed → error；其余 malformed → warn（两文案分级不变）。
	if lastMalformed || anyTruncMalformed {
		if anyTruncMalformed || lastStop == "max_tokens" {
			msg := fmt.Sprintf("[symdig] 提案 AI 输出批内被长度截断（覆盖 %d/%d 品种，提案集不完整）", len(covered), len(sum.Rows))
			log.Print(msg)
			if deps.Store != nil {
				_ = deps.Store.InsertAlert(ctx, nil, "error", msg)
			}
		} else {
			log.Printf("[symdig] proposals fenced 未闭合（EOF 容忍解析）")
			if deps.Store != nil {
				_ = deps.Store.InsertAlertDedup(ctx, "warn", "[symdig] 提案 AI 输出格式异常（已按截断内容容忍解析，提案集可能不完整）", 24*time.Hour)
			}
		}
	}
	if len(all) == 0 {
		return nil
	}
	return appendInbox(deps.InboxPath, runTS.Format("2006-01-02"), "symdig_alias", all)
}

// parseProposals 提取 fenced JSON 数组（未闭合块 EOF 容忍）。
func parseProposals(aiText string) ([]proposal, bool) {
	body, unclosed := "", false
	if m := proposalsRe.FindStringSubmatch(aiText); m != nil {
		body = m[1]
	} else if m := openPropRe.FindStringSubmatch(aiText); m != nil {
		body, unclosed = m[1], true
	} else {
		return nil, false
	}
	var props []proposal
	if err := json.Unmarshal([]byte(body), &props); err != nil {
		// 未闭合 JSON 容忍：逐行试解对象（容忍行首 [ 与行尾逗号）。
		for _, ln := range strings.Split(body, "\n") {
			ln = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(ln, "["), ","))
			if !strings.HasPrefix(ln, "{") {
				continue
			}
			var p proposal
			if err := json.Unmarshal([]byte(ln), &p); err == nil && p.Symbol != "" {
				props = append(props, p)
			}
		}
		return props, true
	}
	return props, unclosed
}

// pendingInboxSymbols 收件箱 pending symdig_alias 提案 symbol 集——跨日
// 全历史行读（行未被归档即 pending；归档文件不查，同 symbol 复活重提合法）。
func pendingInboxSymbols(path string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, ln := range strings.Split(string(data), "\n") {
		cells := strings.Split(ln, "|")
		if len(cells) < 4 || strings.TrimSpace(cells[2]) != "symdig_alias" {
			continue
		}
		title := strings.TrimSpace(cells[3])
		if i := strings.Index(title, "→"); i > 0 {
			out[title[:i]] = true
		}
	}
	return out
}

// appendInbox 提案进收件箱（v2 行走 inboxfmt；本函数保留 conf 过滤+当日同 symbol 去重）。
func appendInbox(path, date, source string, props []proposal) error {
	// conf=0.00 提案直接不入箱；同 symbol 当日 pending 提案已存在则抑制（收件箱去重）。
	var kept []proposal
	for _, p := range props {
		if p.Confidence <= 0 {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return nil
	}
	existing := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		for _, ln := range strings.Split(string(data), "\n") {
			cells := strings.Split(ln, "|")
			if len(cells) >= 4 && strings.TrimSpace(cells[1]) == date && strings.TrimSpace(cells[2]) == source {
				title := strings.TrimSpace(cells[3])
				if i := strings.Index(title, "→"); i > 0 {
					existing[title[:i]] = true
				}
			}
		}
	}
	var rows []inboxfmt.Row
	for _, p := range kept {
		if existing[p.Symbol] {
			continue
		}
		existing[p.Symbol] = true
		rows = append(rows, inboxfmt.Row{
			Source: source, Title: p.Symbol + "→" + p.ProposedCanonical + "@" + p.Venue,
			Reason: fmt.Sprintf("%s conf=%.2f", p.Evidence, p.Confidence),
			Action: "人工核对后落 fin_symbol_aliases/broker_symbol_map",
		})
	}
	return inboxfmt.Append(path, date, rows)
}

// countCovered 批内被提案覆盖品种数（异常批落空计数用）。
func countCovered(props []proposal, batch []pgstore.SymdigFinding) int {
	set := map[string]bool{}
	for _, p := range props {
		set[p.Symbol] = true
	}
	n := 0
	for _, b := range batch {
		if set[b.Symbol] {
			n++
		}
	}
	return n
}
