// parse.go LLM 提案块解析+校验（D-513 设计稿 §2）：
// protojson 严格模式（DiscardUnknown=false——大小写敏感+未知字段拒收，D-505 族事故
// 构造性消除）；非法行整行丢弃计 drop（fail-closed，非拒收整批）；
// 单次>10 条=批级违例整批丢弃；截断/未闭合 fenced 块=批解析失败零落库（A6 红测面）。
package explorer

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	explorerv1 "arb/internal/gen/arb/explorer/v1"
	"arb/internal/store/pgstore"
)

// proposalsFenceRe ```explorer-proposals 块（要求闭合 ```——未闭合=截断，整批拒收）。
var proposalsFenceRe = regexp.MustCompile("(?s)```explorer-proposals\\s*\n(.*?)```")

// pairRe best_pair 键形：venue_a×venue_b（非空段×非空段，×=U+00D7）。
var pairRe = regexp.MustCompile(`^[^×\s]+×[^×\s]+$`)

// maxBatchProposals 单次提案硬上限（设计稿 §2：>10 条=非法）。
const maxBatchProposals = 10

// maxDirectionRunes direction ≤16 字。
const maxDirectionRunes = 16

// parseBatch 严格解析 LLM 输出中的提案块。
// 返回 (batch, ok)：ok=false=无块/截断/protojson 拒收/超上限——整批不落库。
func parseBatch(aiText string) (*explorerv1.ProposalBatch, bool) {
	m := proposalsFenceRe.FindStringSubmatch(aiText)
	if m == nil {
		return nil, false
	}
	var b explorerv1.ProposalBatch
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(
		[]byte(m[1]), &b); err != nil {
		return nil, false
	}
	if len(b.Proposals) > maxBatchProposals {
		return nil, false
	}
	return &b, true
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
			Rationale: cleanCell(p.GetRationale()),
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
