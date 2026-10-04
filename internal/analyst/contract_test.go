package analyst

import (
	"strings"
	"testing"
)

// TestSystemPromptContractAnchors D-065：五契约（8-12）关键词锚防漂移——
// self-check（交稿自验）/ 证伪（判断附证伪条件）/ 触发点（待观察带触发点）。
func TestSystemPromptContractAnchors(t *testing.T) {
	for _, kw := range []string{"self-check", "证伪", "触发点"} {
		if !strings.Contains(SystemPrompt, kw) {
			t.Fatalf("SystemPrompt 缺契约锚 %q", kw)
		}
	}
}
