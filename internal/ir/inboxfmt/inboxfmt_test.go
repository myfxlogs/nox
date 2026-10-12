package inboxfmt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 锚：v2 行七列 pending；Backlog 未分诊语义——accepted 不计、
// 无 status 列旧五列行照计、done 不计。
func TestBacklogSemantics(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox.md")
	if err := Append(inbox, "2026-09-28", []Row{{Source: SrcSleepMoney, Title: "binance/spot", Reason: "r", Action: "a"}}); err != nil {
		t.Fatal(err)
	}
	legacy := "| 2026-09-20 | symdig_alias | OLD→XAU@v | r | a |\n"
	triaged := "| 2026-09-21 | sysretro-weekly | t | r | a | accepted | 已收编 |\n" +
		"| 2026-09-22 | sleep_money | t2 | r | a | done | 完结 |\n"
	if err := Append(inbox, "x", nil); err != nil { // 空集 no-op
		t.Fatal(err)
	}
	f, _ := os.OpenFile(inbox, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(legacy + triaged)
	f.Close()
	if n := Backlog(inbox); n != 2 { // v2 pending×1 + legacy×1
		data, _ := os.ReadFile(inbox)
		t.Fatalf("Backlog=%d want 2:\n%s", n, data)
	}
	// 行格式 v2 七列校验
	data, _ := os.ReadFile(inbox)
	line := strings.SplitN(string(data), "\n", 2)[0]
	if got := len(strings.Split(line, "|")) - 2; got != 7 {
		t.Fatalf("v2 行应 7 列: %q", line)
	}
}
