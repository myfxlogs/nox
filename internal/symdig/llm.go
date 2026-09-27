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
	"sync"
	"time"

	"arb/internal/analyst"
	"arb/internal/store/pgstore"
)

// maxLLMRows 单轮深挖行数上限（防 token 爆）。
const maxLLMRows = 30

const symdigSystemPrompt = `你是 ARB 系统的 symbol 配对深挖员。用户消息给出 MT 经纪商未配对/疑似错配品种清单（JSON）。
任务：对每个品种判断是否存在语义别名可配对（例：GOLD→XAU、GER40→DE30、PLATINUM→XPT、WS30→US30、NIKKEI↔JP225）或明显的映射漂移。
只输出一个 ` + "```symdig-proposals" + ` fenced JSON 数组，每项 {"symbol","venue","proposed_canonical","evidence","confidence"}；
evidence 必须基于给定字段（品种名/canonical/venue），禁止纯猜测编造。无提案输出空数组。块外文字不解析。`

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
	var picks []pgstore.SymdigFinding
	for _, r := range sum.Rows {
		if r.Klass == KlassUnpaired || r.Klass == KlassUnmappedUnknown || r.Klass == KlassMispaired {
			picks = append(picks, r)
			if len(picks) >= maxLLMRows {
				break
			}
		}
	}
	if len(picks) == 0 {
		return nil
	}
	// D-530：max_tokens 且未闭合 → 剩余未覆盖子集再调一轮（上限两轮防循环，合并提案）；
	// 第二轮仍截断 → 告警升级 error；正常闭合输出保持单轮。
	var all []proposal
	covered := map[string]bool{}
	truncated := false
	for round := 0; round < 2 && len(picks) > 0; round++ {
		payload, _ := json.Marshal(picks)
		var aiText strings.Builder
		res, err := deps.Run(ctx, deps.Client, nil, nil, symdigSystemPrompt,
			[]analyst.Message{analyst.TextContent("user", "未配对/错配品种清单：\n```json\n"+string(payload)+"\n```")},
			analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
		if err != nil {
			return fmt.Errorf("analyst run: %w", err)
		}
		props, malformed := parseProposals(aiText.String())
		all = append(all, props...)
		for _, p := range props {
			covered[p.Symbol] = true
		}
		truncated = res.StopReason == "max_tokens" && malformed
		if !truncated {
			if malformed {
				log.Printf("[symdig] proposals fenced 未闭合（EOF 容忍解析）")
				if deps.Store != nil {
					_ = deps.Store.InsertAlert(ctx, nil, "warn", "[symdig] proposals fenced 块未闭合（EOF 截断容忍解析）")
				}
			}
			break
		}
		var rest []pgstore.SymdigFinding
		for _, r := range picks {
			if !covered[r.Symbol] {
				rest = append(rest, r)
			}
		}
		log.Printf("[symdig] 第 %d 轮 max_tokens 截断，剩余 %d 品种进第 %d 轮", round+1, len(rest), round+2)
		picks = rest
	}
	if truncated {
		msg := fmt.Sprintf("[symdig] proposals 二轮后仍 max_tokens 截断（覆盖 %d/%d 品种，提案集不完整）", len(covered), len(sum.Rows))
		log.Print(msg)
		if deps.Store != nil {
			_ = deps.Store.InsertAlert(ctx, nil, "error", msg)
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

var symdigInboxMu sync.Mutex

// appendInbox 提案进收件箱（| date | source | title | reason | action | 行，同 sysretro 口径）。
func appendInbox(path, date, source string, props []proposal) error {
	// D-508 S13：conf=0.00 提案直接不入箱；同 symbol 当日 pending 提案已存在则抑制（收件箱去重）。
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
	var b strings.Builder
	for _, p := range kept {
		if existing[p.Symbol] {
			continue
		}
		existing[p.Symbol] = true
		fmt.Fprintf(&b, "| %s | %s | %s→%s@%s | %s | 人工核对后落 fin_symbol_aliases/broker_symbol_map |\n",
			date, source, cleanCell(p.Symbol), cleanCell(p.ProposedCanonical), cleanCell(p.Venue),
			cleanCell(fmt.Sprintf("%s conf=%.2f", p.Evidence, p.Confidence)))
	}
	symdigInboxMu.Lock()
	defer symdigInboxMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

func cleanCell(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}
