package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitFixture 临时仓库：master 两提交 + wip/done 已并入 + wip/open 未并入。
// 返回 repo 径 + 已并入 hash + 未并入 hash。
func gitFixture(t *testing.T) (repo, mergedHash, openHash string) {
	t.Helper()
	repo = t.TempDir()
	sh := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sh("init", "-b", "master")
	write("a.txt", "1")
	sh("add", "a.txt")
	sh("commit", "-m", "c1")
	// wip/done 分支提交后 merge 回 master → 祖先。
	sh("checkout", "-b", "wip/done")
	write("b.txt", "2")
	sh("add", "b.txt")
	sh("commit", "-m", "c2")
	mergedHash = strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	sh("checkout", "master")
	sh("merge", "--no-ff", "-m", "Merge wip/done: 呆行自动扫描件完工", "wip/done")
	// wip/open 分支提交不 merge → 非祖先。
	sh("checkout", "-b", "wip/open", "master")
	write("c.txt", "3")
	sh("add", "c.txt")
	sh("commit", "-m", "c3")
	openHash = strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	sh("checkout", "master")
	return repo, mergedHash, openHash
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

func TestVerdict_MergedBranchAndHash_Stale(t *testing.T) {
	repo, merged, open := gitFixture(t)
	anc, n := verdict(repo, "master", []string{"wip/done", merged})
	if anc == "" || n != 2 {
		t.Fatalf("merged refs 应判呆行: anc=%q n=%d", anc, n)
	}
	if anc, _ := verdict(repo, "master", []string{"wip/open"}); anc != "" {
		t.Fatal("未并入分支不得判呆行")
	}
	if anc, _ := verdict(repo, "master", []string{open}); anc != "" {
		t.Fatal("未并入 hash 不得判呆行")
	}
	if anc, _ := verdict(repo, "master", []string{"wip/done", open}); anc != "" {
		t.Fatal("混合引用（并入+未并入）保守不判")
	}
	// 不可解析引用（日期串假阳）不计入判定。
	anc, n = verdict(repo, "master", []string{"20261019", merged})
	if anc == "" || n != 1 {
		t.Fatalf("日期串应滤除: anc=%q n=%d", anc, n)
	}
	// 空引用=无拓扑证据不核。
	if _, n := verdict(repo, "master", nil); n != 0 {
		t.Fatalf("空引用 resolved 应=0 得 %d", n)
	}
}

func TestParseRows_TerminalSkipAndRefExtract(t *testing.T) {
	src := `| **甲票** | ✅核验完（并入 abc1234） | 实施席 | done |
| **乙票** | 施工完·报验候裁 | 实施席 | wip/x-1 @deadbeef1 并入 |
| 非表行 |
> 注记块内含 hash ffffffff 不得入票行
| **丙票** | 🔨待领 | 实施席 | 无引用 |`
	rows := parseRows(src)
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	if rows[0].title != "**甲票**" || rows[1].status != "施工完·报验候裁" {
		t.Fatalf("解析错: %+v", rows[:2])
	}
	if rows[0].lineno != 1 {
		t.Fatalf("lineno=%d want 1", rows[0].lineno)
	}
	refs := extractRefs(rows[1].title + " " + rows[1].status + " " + rows[1].detail)
	if len(refs) != 2 || refs[0] != "wip/x-1" || refs[1] != "deadbeef1" {
		t.Fatalf("refs=%v", refs)
	}
	if refs := extractRefs(rows[2].detail); len(refs) != 0 {
		t.Fatalf("丙票应无引用得 %v", refs)
	}
}

func TestTitleKey(t *testing.T) {
	cases := map[string]string{
		"**STATE 呆行自动扫描件（决策席立项**":    "STATE",
		"**TimescaleDB 迁移施工**":       "TimescaleDB",
		"~~待议·冻结~~ **新品类自发现管线（设计稿**": "",
		"**GetOverviewBundle 并行化**":  "GetOverviewBundle",
	}
	for in, _ := range cases {
		t.Logf("%q → %q", in, titleKey(in))
	}
	if k := titleKey("**GetOverviewBundle 并行化**"); k != "GetOverviewBundle" {
		t.Fatalf("key=%q", k)
	}
	// ≥6 rune 阈：STATE(5) 不启用。
	if k := titleKey("**STATE 呆行自动扫描件（决策席立项**"); k != "" {
		t.Fatalf("短键应不启用得 %q", k)
	}
	if k := titleKey("~~待议·冻结~~ **新品类自发现管线（设计稿**"); k != "新品类自发现管线" {
		t.Fatalf("装饰剥除错得 %q", k)
	}
}

// run 端到端：临时库 STATE（经 -state 覆写）+ 已并入引用行/票名反查行 → 呆行候选检出。
func TestRun_DryDetectsStale(t *testing.T) {
	repo, merged, _ := gitFixture(t)
	state := `| **活票** | 施工完·报验 | 实施席 | wip/done 并入 @` + merged + ` |
| **呆行自动扫描件** | 立项待派 | 实施席 | 无引用——票名反查径 |
| **未并票** | 施工中 | 实施席 | wip/open 待并 |`
	sf := filepath.Join(t.TempDir(), "STATE.md")
	if err := os.WriteFile(sf, []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(repo, sf, "master", false, true); err != nil {
		t.Fatalf("run: %v", err)
	}
	// 票名反查锚：merge commit message 含「呆行自动扫描件」→ titleKey 命中。
	hit, err := git(repo, "log", "--merges", "-n", "1", "--grep", "呆行自动扫描件", "master")
	if err != nil || len(hit) == 0 {
		t.Fatalf("票名反查应命中: %v %s", err, hit)
	}
}
