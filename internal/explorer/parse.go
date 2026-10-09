// parse.go LLM 提案块解析+校验（D-513 设计稿 §2）：
// protojson 严格模式（DiscardUnknown=false——大小写敏感+未知字段拒收，D-505 族事故
// 构造性消除）；非法行整行丢弃计 drop（fail-closed，非拒收整批）；
// 单次>10 条=D-078 起截断留前 10 条+log 警示（原「整批丢弃」对良性超限过脆——
// 2026-10-06 双模型同形态实锤 11 条全废；截断条数由调用方并入 drop 保真记账）；
// 截断/未闭合 fenced 块=批解析失败零落库（A6 红测面，真截断仍 fail-closed）。
package explorer

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	explorerv1 "arb/internal/gen/arb/explorer/v1"
	"arb/internal/inboxfmt"
	"arb/internal/store/pgstore"
)

// proposalsFenceRe ```explorer-proposals 块（要求闭合 ```——未闭合=截断，整批拒收）。
var proposalsFenceRe = regexp.MustCompile("(?s)```explorer-proposals\\s*\n(.*?)```")

// pairRe best_pair 键形：venue_a×venue_b（非空段×非空段，×=U+00D7）。
var pairRe = regexp.MustCompile(`^[^×\s]+×[^×\s]+$`)

// maxBatchProposals 单次提案硬上限（设计稿 §2：>10 条=批违例）。
const maxBatchProposals = 10

// maxDirectionRunes direction ≤16 字。
const maxDirectionRunes = 16

// parseBatch 严格解析 LLM 输出中的提案块。
// 返回 (batch, truncDrop, ok)：ok=false=无块/截断/protojson 拒收——整批不落库；
// ok=true 时 truncDrop=超限截断条数（proposals+探针提案合计，调用方并入 drop 记账）。
// D-078：超上限由「整批拒收」（D-513/D-048 防滥发闸）改截断留前 10 条+log 警示——
// 防滥发本义=防告警风暴，截断已足；良性超限（周一开盘 universe 候选增多）不应整批废。
func parseBatch(aiText string) (*explorerv1.ProposalBatch, int, bool) {
	b, t, ok, _ := parseBatchDiag(aiText)
	return b, t, ok
}

// parseBatchDiag parseBatch + 失败分型（D-143：告警文案/补救轮观测消费）：
// failKind ∈ "no-fence"（无/未闭合 fenced 块）|"unmarshal"（protojson 拒收，sanitize 已试）；
// ok=true 时空串。
func parseBatchDiag(aiText string) (*explorerv1.ProposalBatch, int, bool, string) {
	m := proposalsFenceRe.FindStringSubmatch(aiText)
	if m == nil {
		return nil, 0, false, "no-fence"
	}
	var b explorerv1.ProposalBatch
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(
		[]byte(m[1]), &b); err != nil {
		// D-081 ①：模型 JSON 尾部偶发机械瑕疵（sensenova 实拍 "probe_proposals":[]}}
		// 多一右括号）→ sanitize 修一次重试；无可修/修后仍坏照拒（fail-closed 保真，
		// 一次机械修正为限——不做多轮/模糊解析）。
		fixed := sanitizeJSONTail(m[1])
		if fixed == m[1] {
			return nil, 0, false, "unmarshal"
		}
		if err2 := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(
			[]byte(fixed), &b); err2 != nil {
			return nil, 0, false, "unmarshal"
		}
		log.Printf("[explorer] parse sanitize 重试成功（尾部花括号修正）")
	}
	trunc := 0
	if n := len(b.Proposals); n > maxBatchProposals {
		trunc += n - maxBatchProposals
		log.Printf("[explorer] 提案批 %d 条超上限 %d——截断留前 %d 条（drop 记 %d）", n, maxBatchProposals, maxBatchProposals, n-maxBatchProposals)
		b.Proposals = b.Proposals[:maxBatchProposals]
	}
	if n := len(b.ProbeProposals); n > maxBatchProposals {
		trunc += n - maxBatchProposals
		log.Printf("[explorer] 探针提案批 %d 条超上限 %d——截断留前 %d 条（drop 记 %d）", n, maxBatchProposals, maxBatchProposals, n-maxBatchProposals)
		b.ProbeProposals = b.ProbeProposals[:maxBatchProposals]
	}
	return &b, trunc, true, ""
}

// sanitizeJSONTail D-081 ①：模型 JSON 尾部机械瑕疵单次修正——只做两种机械动作：
// 右括号过剩（串尾剥差额个 }）与缺失（补差额个）；其余情形原样返回（含尾随杂字符
// 但括号平衡、结构真坏如键缺冒号——过度启发=伪装解析器，不做）。字符串内花括号会
// 扰动计数：接受——本函数只在「已解析失败」的载荷上尝试，修坏=重试再拒，零风险外溢。
func sanitizeJSONTail(s string) string {
	trimmed := strings.TrimRight(s, " \t\r\n")
	drop := strings.Count(trimmed, "}") - strings.Count(trimmed, "{")
	add := strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
	switch {
	case drop > 0 && strings.HasSuffix(trimmed, strings.Repeat("}", drop)):
		return trimmed[:len(trimmed)-drop]
	case add > 0:
		return trimmed + strings.Repeat("}", add)
	}
	return s
}

// validateProposals 行级 fail-closed 校验：返回合法行集 + drop 计数。
// 规则：canonical 必须命中已知集；best_pair 空或 venue×venue 键形；direction ≤16 字。
func validateProposals(b *explorerv1.ProposalBatch, known map[string]bool) (rows []pgstore.ExplorerProposal, drop int) {
	for _, p := range b.GetProposals() {
		canonical := strings.TrimSpace(p.GetCanonical())
		if canonical == "" || !known[canonical] {
			drop++
			continue
		}
		bp := strings.TrimSpace(p.GetBestPair())
		if bp != "" && !pairRe.MatchString(bp) {
			drop++
			continue
		}
		dir := strings.TrimSpace(p.GetDirection())
		if utf8.RuneCountInString(dir) > maxDirectionRunes {
			drop++
			continue
		}
		ev := json.RawMessage(`{}`)
		if p.GetEvidence() != nil {
			if raw, err := p.GetEvidence().MarshalJSON(); err == nil && len(raw) > 0 {
				ev = raw
			}
		}
		rows = append(rows, pgstore.ExplorerProposal{
			Canonical: canonical,
			BestPair:  bp,
			Direction: dir,
			Rationale: inboxfmt.CleanCell(p.GetRationale()),
			Evidence:  ev,
		})
	}
	return rows, drop
}

// extractNarrative batch 叙事（≤300 字截断，收件箱行用）。
func extractNarrative(b *explorerv1.ProposalBatch) string {
	n := strings.TrimSpace(b.GetNarrative())
	if utf8.RuneCountInString(n) > 300 {
		n = string([]rune(n)[:300])
	}
	return n
}
