package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- S2 分类键提取单测 ---

func TestClassifyAutotradeLog_KnownPrefixes(t *testing.T) {
	cases := []struct{ reason, want string }{
		{"daily loss halt triggered", "daily loss"},
		{"max positions reached", "max positions"},
		{"disabled by config", "disabled"},
		{"duplicate dedup key", "duplicate"},
		{"balance too low", "balance"},
		{"system state=DAILY_LOSS_HALT (not RUNNING)", "state"},
		// 修复⑭ S7 噪音映射：per-qty/per-venue 数值噪音 → 单一分类键
		{"leg notional imbalance: buy $1000.00 vs sell $877.77 (12.22% > 2.0%)", "leg notional imbalance"},
		{"execute failed (pipeline returned FAILED)", "execute failed"},
		{"quote stale or net benefit degraded beyond threshold", "quote stale"},
		{"qty below volume step: buy 0.000000 sell 0.000000 (step too small)", "below"},
		{"qty 0.03 below venue min lot 0.10 for 52993526:mt5 USTEC", "below"},
	}
	for _, c := range cases {
		if got := classifyAutotradeLog(c.reason); got != c.want {
			t.Errorf("classifyAutotradeLog(%q)=%q, want %q", c.reason, got, c.want)
		}
	}
}

func TestClassifyAutotradeLog_UnknownFallback(t *testing.T) {
	got := classifyAutotradeLog("some novel reason never seen before")
	if len([]rune(got)) > 30 {
		t.Errorf("unknown fallback should truncate to 30 runes, got %d", len([]rune(got)))
	}
	// 30 rune 截断 = "some novel reason never seen b"（含空格）
	if got != "some novel reason never seen b" {
		t.Errorf("classifyAutotradeLog unknown want 30-rune prefix, got %q", got)
	}
}

func TestClassifyAlert_FirstSegmentBeforeColon(t *testing.T) {
	cases := []struct{ msg, want string }{
		{"对账不一致：venue 1314615 持仓 ticket=55257993", "对账不一致"},
		{"reconcile failed: timeout", "reconcile failed"},
		{"对账不一致,venue REDACTED_MT_ACCT", "对账不一致"},
		// 修复⑭ S7 噪音映射 + 返修包① T2：LEGGED → cross-broker-cfd-basis-risk 条目键
		{"position 1 LEGGED: 1/2 legs filled", "cross-broker-cfd-basis-risk"},
		{"position 67 LEGGED: 1/2 legs filled", "cross-broker-cfd-basis-risk"},
		{"持仓 #2 LEGGED：单腿暴露，需手动处置", "cross-broker-cfd-basis-risk"},
		{"持仓 #282 LEGGED：单腿暴露，需手动处置", "cross-broker-cfd-basis-risk"},
	}
	for _, c := range cases {
		if got := classifyAlert(c.msg); got != c.want {
			t.Errorf("classifyAlert(%q)=%q, want %q", c.msg, got, c.want)
		}
	}
}

func TestClassifyAlert_NoSeparatorFallback(t *testing.T) {
	got := classifyAlert("shortnovelmessage")
	if got != "shortnovelmessage" {
		t.Errorf("classifyAlert no-separator want 'shortnovelmessage', got %q", got)
	}
}

// --- S2.3 匹配单测 ---

func TestMatchEntry_Hit(t *testing.T) {
	rows := []indexRow{
		{id: "auth-header-curl-verbose-leak", keywords: "curl -sv Authorization Bearer transcript"},
		{id: "loop-initial-block-no-timeout", keywords: "StartLoop 首轮阻塞 无超时"},
	}
	id, ok := matchEntry("curl", rows)
	if !ok || id != "auth-header-curl-verbose-leak" {
		t.Errorf("matchEntry(curl) want hit auth-header-curl-verbose-leak, got %q ok=%v", id, ok)
	}
}

func TestMatchEntry_Miss(t *testing.T) {
	rows := []indexRow{{id: "auth-header-curl-verbose-leak", keywords: "curl"}}
	_, ok := matchEntry("nonexistent-keyword", rows)
	if ok {
		t.Error("matchEntry with nonexistent keyword should miss")
	}
}

// --- 返修包① T2：忽略表 + LEGGED→条目命中单测 ---

// TestIsIgnored_IgnoreTable 风控正常触发类三键在忽略表 → isIgnored=true（零信号不进待归纳）。
func TestIsIgnored_IgnoreTable(t *testing.T) {
	for _, k := range []string{"max positions", "daily loss", "state"} {
		if !isIgnored(k) {
			t.Errorf("isIgnored(%q) want true (风控正常触发类应忽略)", k)
		}
	}
}

// TestIsIgnored_NonIgnored 非忽略键 → isIgnored=false（进待归纳或条目）。
func TestIsIgnored_NonIgnored(t *testing.T) {
	for _, k := range []string{"legged", "below", "execute failed", "quote stale", "novel-key"} {
		if isIgnored(k) {
			t.Errorf("isIgnored(%q) want false (非忽略键)", k)
		}
	}
}

// TestMatchEntry_LEGGEDHitEntry LEGGED 分类键=cross-broker-cfd-basis-risk → matchEntry 真命中该条目。
func TestMatchEntry_LEGGEDHitEntry(t *testing.T) {
	rows := []indexRow{
		{id: "cross-broker-cfd-basis-risk", keywords: "跨broker基差 CFD对冲 AUDJPY"},
	}
	id, ok := matchEntry("cross-broker-cfd-basis-risk", rows)
	if !ok || id != "cross-broker-cfd-basis-risk" {
		t.Errorf("matchEntry(cross-broker-cfd-basis-risk) want hit cross-broker-cfd-basis-risk, got %q ok=%v", id, ok)
	}
}

// --- S3.1 正文追加重现行单测 ---

func TestAppendEvidenceLine_HasEvidenceSection(t *testing.T) {
	content := "## 证据\n- existing ref\n\n## 反证条件\n- check: foo\n"
	got, ok := appendEvidenceLine(content, "2026-08-21", "test summary", "autotrade_logs#100")
	if !ok {
		t.Fatal("should append when ## 证据 section exists")
	}
	if !contains(got, "- 2026-08-21 重现: test summary | ref: autotrade_logs#100") {
		t.Errorf("appended line missing or wrong:\n%s", got)
	}
	// 追加位置应在 ## 反证条件 之前
	evIdx := indexOf(got, "- 2026-08-21 重现")
	revIdx := indexOf(got, "## 反证条件")
	if evIdx < 0 || revIdx < 0 || evIdx > revIdx {
		t.Errorf("appended line should be before ## 反证条件; ev=%d rev=%d", evIdx, revIdx)
	}
}

func TestAppendEvidenceLine_NoEvidenceSection(t *testing.T) {
	content := "## 事实\nfoo\n\n## 处置\nbar\n"
	_, ok := appendEvidenceLine(content, "2026-08-21", "test", "ref")
	if ok {
		t.Error("should return ok=false when no ## 证据 section (skip, don't invent)")
	}
}

func TestAppendEvidenceLine_EvidenceIsLastSection(t *testing.T) {
	content := "## 证据\n- existing\n"
	got, ok := appendEvidenceLine(content, "2026-08-21", "test", "ref")
	if !ok {
		t.Fatal("should append when ## 证据 is last section")
	}
	if !contains(got, "- 2026-08-21 重现: test | ref: ref") {
		t.Errorf("appended line missing:\n%s", got)
	}
}

func TestAppendEvidenceLine_SummaryTruncated(t *testing.T) {
	content := "## 证据\n- existing\n"
	// 61 字符 → 触发 60 字符截断 + …
	long := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	got, _ := appendEvidenceLine(content, "2026-08-21", long, "ref")
	if !contains(got, "…") {
		t.Errorf("long summary (>60 runes) should be truncated with …:\n%s", got)
	}
}

// --- S3.2 待归纳聚合 + ⚠ 重复标记单测 ---

func TestRenderPendingMd_RepeatMark(t *testing.T) {
	events := []pendingEvent{
		{Date: "2026-08-20", Source: "autotrade_logs", ClassKey: "novel-key-a", Summary: "novel event A", Count: 1},
		{Date: "2026-08-21", Source: "autotrade_logs", ClassKey: "novel-key-a", Summary: "novel event A", Count: 1},
	}
	md := renderPendingMd(events, nil, nil)
	if !contains(md, "⚠ 重复") {
		t.Errorf("repeat mark should appear for count>=2:\n%s", md)
	}
	if !contains(md, "| 2 ⚠ 重复 |") {
		t.Errorf("count should be 2 with repeat mark:\n%s", md)
	}
}

func TestRenderPendingMd_NoRepeatForSingle(t *testing.T) {
	events := []pendingEvent{
		{Date: "2026-08-21", Source: "alerts", ClassKey: "novel-key-b", Summary: "novel event B", Count: 1},
	}
	md := renderPendingMd(events, nil, nil)
	if contains(md, "⚠ 重复") {
		t.Errorf("single event should NOT have repeat mark:\n%s", md)
	}
}

func TestRenderPendingMd_CrossDayAggregation(t *testing.T) {
	// 历史事件 + 本次事件同 classKey → 聚合计数
	history := []pendingEvent{
		{Date: "2026-08-19", Source: "autotrade_logs", ClassKey: "novel-key-c", Summary: "novel C", Count: 1},
	}
	events := []pendingEvent{
		{Date: "2026-08-21", Source: "autotrade_logs", ClassKey: "novel-key-c", Summary: "novel C", Count: 1},
	}
	md := renderPendingMd(events, history, nil)
	if !contains(md, "| 2 ⚠ 重复 |") {
		t.Errorf("cross-day aggregation should yield count=2 with repeat mark:\n%s", md)
	}
}

// TestRenderPendingMd_HistoryCountPreserved 返修包① T3：历史行携带 Count 不再被清零。
// history Count=405 + 1 新事件 → render 含 406（原 bug：Count++/初值 1 → 清零为 1）。
func TestRenderPendingMd_HistoryCountPreserved(t *testing.T) {
	history := []pendingEvent{
		{Date: "2026-08-19", Source: "autotrade_logs", ClassKey: "flood-key", Summary: "flood event", Count: 405},
	}
	events := []pendingEvent{
		{Date: "2026-08-22", Source: "autotrade_logs", ClassKey: "flood-key", Summary: "flood event", Count: 1},
	}
	md := renderPendingMd(events, history, nil)
	if !contains(md, "| 406 ⚠ 重复 |") {
		t.Errorf("history Count=405 + 1 new should yield 406 (T3 fix preserves history Count):\n%s", md)
	}
}

// --- S3.3 staleness 单测（注入时钟） ---

func TestParseStaleness_Over30Days(t *testing.T) {
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	entries := []frontmatterEntry{
		{ID: "stale-entry", LastVerified: "2026-07-15", EvidenceRefs: []map[string]interface{}{{"type": "server_fact"}}},
		{ID: "fresh-entry", LastVerified: "2026-08-20", EvidenceRefs: []map[string]interface{}{{"type": "server_fact"}}},
	}
	out := parseStaleness(entries, now)
	if len(out) != 1 || out[0].ID != "stale-entry" {
		t.Errorf("stale entry (37 days) should be reported, fresh (1 day) not; got %v", out)
	}
	if out[0].DaysSince != 37 {
		t.Errorf("stale entry days since want 37, got %d", out[0].DaysSince)
	}
}

func TestParseStaleness_NoServerFact(t *testing.T) {
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	entries := []frontmatterEntry{
		{ID: "doc-only", LastVerified: "2026-06-01", EvidenceRefs: []map[string]interface{}{{"type": "doc"}}},
	}
	out := parseStaleness(entries, now)
	if len(out) != 0 {
		t.Errorf("entry without server_fact should not be reported; got %v", out)
	}
}

// --- S3.4 水位幂等单测 ---

func TestStateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".digest_state")
	// 首次读 → 零水位
	s, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.AutotradeLogsMaxID != 0 || s.AlertsMaxID != 0 {
		t.Errorf("first load should be zero, got %+v", s)
	}
	// 写水位
	s.AutotradeLogsMaxID = 100
	s.AlertsMaxID = 200
	if err := saveState(path, s); err != nil {
		t.Fatal(err)
	}
	// 再读 → 同值
	s2, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.AutotradeLogsMaxID != 100 || s2.AlertsMaxID != 200 {
		t.Errorf("reload should match saved; got %+v", s2)
	}
	// 文件应存在（git 跟踪）
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file should exist after save: %v", err)
	}
}

// --- helpers ---

func contains(s, sub string) bool { return indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- H3 gitCommitKB 单测 ---

// initGitRepo 在临时目录初始化一个 git repo + docs/经验库/ 目录，返回 kbDir 绝对路径。
func initGitRepo(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "test@digest"}, {"config", "user.name", "digest-test"},
	} {
		cmdArgs := append([]string{"-C", repoRoot}, args...)
		if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	kbDir := filepath.Join(repoRoot, "docs", "经验库")
	if err := os.MkdirAll(kbDir, 0755); err != nil {
		t.Fatalf("mkdir kb: %v", err)
	}
	// 初始 commit（空 repo 不能直接 add 目录）
	if err := os.WriteFile(filepath.Join(repoRoot, ".gitkeep"), []byte(""), 0644); err != nil {
		t.Fatalf("write gitkeep: %v", err)
	}
	if out, err := exec.Command("git", "-C", repoRoot, "add", ".gitkeep").CombinedOutput(); err != nil {
		t.Fatalf("git add gitkeep: %v\n%s", err, string(out))
	}
	if out, err := exec.Command("git", "-C", repoRoot, "commit", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git init commit: %v\n%s", err, string(out))
	}
	return kbDir
}

// lastCommitMessage 取 repo 最近一条 commit message。
func lastCommitMessage(t *testing.T, repoRoot string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "log", "-1", "--format=%s").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// TestGitCommitKB_WithChanges 有变更 → 自动 commit + message 含日期/hits/pending。
func TestGitCommitKB_WithChanges(t *testing.T) {
	kbDir := initGitRepo(t)
	// 写一个条目文件制造变更
	if err := os.WriteFile(filepath.Join(kbDir, "test-entry.md"), []byte("# test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := gitCommitKB(kbDir, "2026-08-21", 3, 5); err != nil {
		t.Fatalf("gitCommitKB: %v", err)
	}
	repoRoot := filepath.Join(kbDir, "..", "..")
	msg := lastCommitMessage(t, repoRoot)
	if !contains(msg, "2026-08-21") || !contains(msg, "3 重现") || !contains(msg, "5 待归纳") {
		t.Errorf("commit message should contain date/hits/pending, got: %q", msg)
	}
}

// TestGitCommitKB_NoChanges 无变更 → 跳过 commit（不报错，HEAD 不变）。
func TestGitCommitKB_NoChanges(t *testing.T) {
	kbDir := initGitRepo(t)
	repoRoot := filepath.Join(kbDir, "..", "..")
	before := lastCommitMessage(t, repoRoot)
	if err := gitCommitKB(kbDir, "2026-08-21", 0, 0); err != nil {
		t.Fatalf("gitCommitKB no changes should not error, got: %v", err)
	}
	after := lastCommitMessage(t, repoRoot)
	if before != after {
		t.Errorf("no changes should not create new commit; before=%q after=%q", before, after)
	}
}

// TestGitCommitKB_RelativePath 修复⑭ S7：kbDir 相对路径时 gitCommitKB 不再因
// filepath.Rel(absolute, relative) 报错（先 Abs(kbDir) 再 Rel）。
func TestGitCommitKB_RelativePath(t *testing.T) {
	kbDir := initGitRepo(t)
	// 切到 repo 根的子目录，使 kbDir 可用相对路径表达（复现 digest.log 报错场景）
	repoRoot := filepath.Join(kbDir, "..", "..")
	subDir := filepath.Join(repoRoot, "tools", "digest")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	oldWd, _ := os.Getwd()
	if err := os.Chdir(subDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)
	relKb := "../../docs/经验库"
	// 写变更
	if err := os.WriteFile(filepath.Join(kbDir, "rel-test.md"), []byte("# rel\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := gitCommitKB(relKb, "2026-08-22", 1, 0); err != nil {
		t.Fatalf("gitCommitKB with relative kbDir should succeed (S7 fix), got: %v", err)
	}
	msg := lastCommitMessage(t, repoRoot)
	if !contains(msg, "2026-08-22") {
		t.Errorf("commit should succeed with relative path; msg=%q", msg)
	}
}

// --- 消解出口（✅ 行尾标记 → resolved 分流 + 归档幂等）单测 ---

// TestParsePendingHistory_SplitsResolved ✅ 标记行分流：active 不含、resolved 带结论与计数（⚠ 与 ✅ 共存）。
func TestParsePendingHistory_SplitsResolved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "待归纳.md")
	md := "# 待归纳事件（digest 自动生成，勿手编）\n\n" +
		"| 日期 | 来源 | 事件摘要 | 次数 |\n" +
		"|------|------|----------|------|\n" +
		"| 2026-09-13 | alerts | repo GC007 spike | 12 |\n" +
		"| 2026-09-12 | autotrade_logs | gate 拒单 | 404 ⚠ 重复 ✅ 噪音判否（D-277-C4） |\n" +
		"| 2026-09-13 | alerts | trx yield drift | 3 ✅ 真信号已登记 |\n"
	if err := os.WriteFile(path, []byte(md), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	active, resolved := parsePendingHistory(path)
	if len(active) != 1 {
		t.Fatalf("active len = %d, want 1", len(active))
	}
	if active[0].Summary != "repo GC007 spike" || active[0].Count != 12 {
		t.Errorf("active[0] = %+v, want repo GC007 spike/12", active[0])
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved len = %d, want 2", len(resolved))
	}
	if resolved[0].Count != 404 || resolved[0].Verdict != "噪音判否（D-277-C4）" {
		t.Errorf("resolved[0] = %+v, want count 404 + verdict 噪音判否（D-277-C4）", resolved[0])
	}
	if resolved[1].Count != 3 || resolved[1].Verdict != "真信号已登记" {
		t.Errorf("resolved[1] = %+v, want count 3 + verdict 真信号已登记", resolved[1])
	}
}

// TestParsePendingHistory_SectionBoundary D-357 T1：事件表恒为文件第一个表——首个 `## `
// 段标题即事件区终点，段内表行（spot-carry/费率轨/覆盖率段）不再被吞入事件表
// （D-356 收编干跑实证噪音：| VTHO_USDT |…、| 492 | CLOSED | carry_death |… 逐日退化）。
// 旧实现红=段表行被解析为 active 事件；事件表真行 ✅/⚠ 语义不变。
func TestParsePendingHistory_SectionBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "待归纳.md")
	md := "# 待归纳事件（digest 自动生成，勿手编）\n\n" +
		"| 日期 | 来源 | 事件摘要 | 次数 |\n" +
		"|------|------|----------|------|\n" +
		"| 2026-09-13 | alerts | repo GC007 spike | 12 |\n" +
		"| 2026-09-12 | autotrade_logs | gate 拒单 | 404 ⚠ 重复 ✅ 噪音判否 |\n" +
		"\n## spot-carry 候选（band 20-300 %/yr，独立排名）\n\n" +
		"| symbol | venue | funding %/yr | net %/yr |\n" +
		"|---|---|---|---|\n" +
		"| VTHO_USDT | gate | -281.85 | 281 ⚠ 重复 |\n" +
		"\n## 带内循环·真实费率轨（D-341 S4）\n\n" +
		"| pos | 状态 | exit_kind | realized$ | estimated carry$ | simulated carry$ | convergence$ |\n" +
		"|------|------|-----------|-----------|------------------|------------------|---------------|\n" +
		"| 492 | CLOSED | carry_death | -0.55 | 4.05 | 0 | 4 ⚠ 重复 |\n"
	if err := os.WriteFile(path, []byte(md), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	active, resolved := parsePendingHistory(path)
	if len(active) != 1 || active[0].Summary != "repo GC007 spike" || active[0].Count != 12 {
		t.Fatalf("active 应仅含事件表真行 1 条, got %d 条: %+v", len(active), active)
	}
	if len(resolved) != 1 || resolved[0].Count != 404 {
		t.Fatalf("resolved 应仅含事件表 ✅ 行 1 条, got %d 条: %+v", len(resolved), resolved)
	}
	for _, e := range active {
		for _, leak := range []string{"VTHO_USDT", "carry_death", "gate | -281.85"} {
			if strings.Contains(e.Summary, leak) || strings.Contains(e.Date, leak) {
				t.Errorf("段表行泄漏进事件表: %q", e.Summary)
			}
		}
	}
}

// TestAppendResolvedArchive_Idempotent 两连跑同签名只写一行（崩溃重跑不双写）。
func TestAppendResolvedArchive_Idempotent(t *testing.T) {
	dir := t.TempDir()
	resolved := []resolvedEvent{
		{Date: "2026-09-12", Source: "autotrade_logs", Summary: "gate 拒单", Count: 404, Verdict: "噪音判否（D-277-C4）"},
	}
	now := time.Date(2026, 9, 14, 15, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := appendResolvedArchive(dir, resolved, now); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	path := filepath.Join(dir, "归档", "待归纳-2026-09.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	content := string(data)
	if got := strings.Count(content, "gate 拒单"); got != 1 {
		t.Errorf("archive rows for sig = %d, want 1（幂等）\n%s", got, content)
	}
	if !strings.Contains(content, "噪音判否（D-277-C4）") || !strings.Contains(content, "| 2026-09-14 |") {
		t.Errorf("archive missing verdict or resolve date\n%s", content)
	}
}

// TestRenderPendingMd_OutletHeader 头部含 ✅ 消解出口说明。
func TestRenderPendingMd_OutletHeader(t *testing.T) {
	md := renderPendingMd(nil, nil, nil)
	if !strings.Contains(md, "✅ 结论") || !strings.Contains(md, "归档/待归纳-") {
		t.Errorf("header missing outlet line:\n%s", md)
	}
}
