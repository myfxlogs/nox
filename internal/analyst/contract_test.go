package analyst

import (
	"strings"
	"testing"
)

// TestSystemPromptContractAnchors D-065/D-099：契约关键词锚防漂移——
// self-check（交稿自验）/ 证伪（判断附证伪条件）/ 触发点（待观察带触发点）/
// 矛盾（矛盾优先）/ 判别（判别性最小实验）/ 分层归因（分层归因）。
func TestSystemPromptContractAnchors(t *testing.T) {
	for _, kw := range []string{"self-check", "证伪", "触发点", "矛盾", "判别", "分层归因"} {
		if !strings.Contains(SystemPrompt, kw) {
			t.Fatalf("SystemPrompt 缺契约锚 %q", kw)
		}
	}
}
