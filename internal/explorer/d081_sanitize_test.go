// d081_sanitize_test.go —— D-081 锚：parse sanitize 单次重试 + canonical 字段语义行级拒。
package explorer

import (
	"strings"
	"testing"
)

// TestD081_SanitizeExtraBraces 多 }：合法 10 条批尾部多 2 个 }（sensenova 实拍形态）
// → sanitize 修一次重试，ok=true、10 条完整、trunc=0。
func TestD081_SanitizeExtraBraces(t *testing.T) {
	inner := strings.TrimSuffix(nProposalBatch(10), "\n```\n")
	b, trunc, ok := parseBatch(inner + "}}" + "\n```\n")
	if !ok || len(b.GetProposals()) != 10 || trunc != 0 {
		t.Fatalf("多 } 批应 sanitize 后过: ok=%v n=%d trunc=%d", ok, len(b.GetProposals()), trunc)
	}
}

// TestD081_SanitizeMissingBrace 缺 }：合法批去掉最后 1 个 } → 补差额后过。
func TestD081_SanitizeMissingBrace(t *testing.T) {
	inner := strings.TrimSuffix(nProposalBatch(3), "\n```\n")
	broken := strings.TrimSuffix(inner, "}") + "\n```\n"
	b, trunc, ok := parseBatch(broken)
	if !ok || len(b.GetProposals()) != 3 || trunc != 0 {
		t.Fatalf("缺 } 批应补齐后过: ok=%v n=%d trunc=%d", ok, len(b.GetProposals()), trunc)
	}
}

// TestD081_SanitizeTrulyBrokenRejected 括号平衡但结构坏（键缺冒号）→ sanitize 无操作
// → ok=false（真坏照拒，fail-closed 保真）。
func TestD081_SanitizeTrulyBrokenRejected(t *testing.T) {
	broken := "```explorer-proposals\n{\"proposals\":[{\"canonical\" \"A\"}]}\n```\n"
	if b, trunc, ok := parseBatch(broken); ok || b != nil || trunc != 0 {
		t.Fatalf("结构真坏批应照拒: ok=%v b=%v trunc=%d", ok, b, trunc)
	}
}

// TestD081_SanitizeBalancedJunkNotApplied 括号平衡但尾随杂字符 → 不修（过度启发不做）。
func TestD081_SanitizeBalancedJunkNotApplied(t *testing.T) {
	broken := "```explorer-proposals\n{\"proposals\":[]}x\n```\n"
	if _, _, ok := parseBatch(broken); ok {
		t.Fatal("括号平衡+尾随杂字符应原样拒收（不修）")
	}
}

// TestD081_CanonicalAccountDescRejected 实拍形态锚：canonical="idle 3001157"（账户
// 描述非品种符号）语法过但 validateProposals 行级 drop（known 集不含）。
func TestD081_CanonicalAccountDescRejected(t *testing.T) {
	raw := "```explorer-proposals\n{\"proposals\":[{\"canonical\":\"idle 3001157\"," +
		"\"best_pair\":\"binance×gate\",\"direction\":\"w\",\"rationale\":\"r\",\"anchor\":\"a\"}]}\n```\n"
	b, _, ok := parseBatch(raw)
	if !ok {
		t.Fatalf("语法合法批应解析成功（行级拒是 validateProposals 域）")
	}
	rows, drop := validateProposals(b, map[string]bool{"XAUUSD": true})
	if len(rows) != 0 || drop != 1 {
		t.Fatalf("canonical=账户描述行应被 drop: rows=%d drop=%d", len(rows), drop)
	}
}
