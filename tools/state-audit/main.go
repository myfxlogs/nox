// tools/state-audit —— STATE 呆行自动扫描（注记60/62 结构性堵口：翻态滞后
// 呆行同日四连实证——jit-sweep/s2-canon/P0/live-equity 均「工作已并入 master
// 而票行未翻态」，靠人翻=纪律失守高发点；改机械日拍自动暴露）。
//
// 逻辑（票面 scope）：
//  1. 读 STATE.md（默认 `git -C <repo> show <master>:docs/handoff/STATE.md`——
//     票行与拓扑同源同刻不受部署滞后影响；-state 可覆写文件径）；
//  2. 解析票行（| **标题** | status | seat | detail |），status 含 ✅=终态跳过；
//     注记块（> 前缀行）天然不入票行格——禁线「禁扫注记块内 hash」结构性满足；
//  3. 非终态行抽 git 引用：wip/<name> 分支令牌 + 裸 commit hash
//     （[0-9a-f]{7,40}，rev-parse 验真滤日期串等假阳）→ 全部可解析引用均为
//     master 祖先 = 呆行候选；**拓扑核不到即不判**（禁线：不误报合法在飞态）；
//  4. 无引用行按票名 `git log --merges <master> --grep=<票名键>` 反查——
//     命中合并 commit = 并入证据（键=标题净化前缀 ≥6 字，防宽匹配误报）；
//  5. 呆行候选写 alerts（warn，dedup 6h）——文案含 STATE 行号+票名+祖先
//     证据 ref，供人工一行翻正；工具只读 STATE 零写（翻正权在席位）。
//
// 用法：state-audit -repo /opt/nox-git [-fetch] [-master master] [-dry]
// 部署面：scripts/state-audit-run.sh（DSN 解析+fetch+执行），build_host_tools.sh 随链。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

var (
	branchRe = regexp.MustCompile(`\bwip/[A-Za-z0-9._-]+`)
	hashRe   = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	// titleCut 票名键截取边界（标题本体止于首处装饰/括号/破折号）。
	titleCut = regexp.MustCompile(`[~*（(\s—：:，,·]`)
	// struckRe 划线标记整段（~~待议·冻结~~=废弃态非票名本体）。
	struckRe = regexp.MustCompile(`~~[^~]*~~`)
)

// row 票行解析结果：lineno 供告警定位人工翻正；引用从行内全格抽
// （hash/分支常落 detail 格「并入 @hash」「wip/x」）。
type row struct {
	lineno int
	title  string
	status string
	detail string
}

func main() {
	repo := flag.String("repo", "/opt/nox-git", "git 仓库（SG 上 bare/mirror clone 径；本机可指 /opt/nox）")
	statePath := flag.String("state", "", "STATE.md 文件径覆写（缺省=git show <master>:docs/handoff/STATE.md）")
	master := flag.String("master", "master", "master 参照 ref")
	fetch := flag.Bool("fetch", false, "扫前 git fetch --all --prune")
	dry := flag.Bool("dry", false, "只打印不库告警")
	flag.Parse()
	if err := run(*repo, *statePath, *master, *fetch, *dry); err != nil {
		log.Fatalf("state-audit: %v", err)
	}
}

// staleHit 呆行候选：lineno 定位+票名+证据描述（并入的 ref 或 merge 命中）。
type staleHit struct {
	lineno   int
	title    string
	evidence string
}

func run(repo, statePath, master string, fetch, dry bool) error {
	ctx := context.Background()
	if fetch {
		if out, err := git(repo, "fetch", "--all", "--prune"); err != nil {
			log.Printf("state-audit: fetch 失败（沿现有拓扑续扫）: %v %s", err, out)
		}
	}
	var src []byte
	var err error
	if statePath != "" {
		src, err = os.ReadFile(statePath)
	} else {
		src, err = git(repo, "show", master+":docs/handoff/STATE.md")
	}
	if err != nil {
		return fmt.Errorf("读 STATE.md: %w", err)
	}
	rows := parseRows(string(src))
	var stales []staleHit
	withRefs := 0
	for _, r := range rows {
		if strings.Contains(r.status, "✅") {
			continue // 终态行不核
		}
		refs := extractRefs(r.title + " " + r.status + " " + r.detail)
		if anc, n := verdict(repo, master, refs); n > 0 {
			withRefs++
			if anc != "" {
				stales = append(stales, staleHit{r.lineno, trunc(r.title, 60), "引用 " + anc + " 已是 master 祖先"})
			}
			continue // 有拓扑证据行不再走票名反查
		}
		// 无引用行：票名 grep 合并 commit 反查（scope②——行未写 hash 但已并入）。
		if key := titleKey(r.title); key != "" {
			if hit, err := git(repo, "log", "--merges", "-n", "1", "--grep", key, master); err == nil && len(hit) > 0 {
				stales = append(stales, staleHit{r.lineno, trunc(r.title, 60), "merge --grep 「" + key + "」命中并入"})
			}
		}
	}
	log.Printf("state-audit: 票行 %d 扫毕，非终态带引用 %d 行，呆行候选 %d", len(rows), withRefs, len(stales))
	for _, m := range stales {
		log.Printf("state-audit: 呆行候选 行%d %s（%s）", m.lineno, m.title, m.evidence)
	}
	if dry || len(stales) == 0 {
		return nil
	}
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		return errors.New("DB_DSN 未设置（-dry 试跑或经 state-audit-run.sh 注入）")
	}
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect pg: %w", err)
	}
	defer st.Close()
	for _, m := range stales {
		msg := fmt.Sprintf("state-audit 呆行候选：行%d 票「%s」%s——疑呆行，翻行=注记60/62 闸",
			m.lineno, m.title, m.evidence)
		if err := st.InsertAlertDedup(ctx, "warn", msg, 6*time.Hour); err != nil {
			return fmt.Errorf("alert 落库: %w", err)
		}
	}
	return nil
}

// parseRows 抽 | **t** | s | seat | d | 形票行——取前 4 个 | 分隔单元格+行号。
func parseRows(src string) []row {
	var out []row
	for i, ln := range strings.Split(src, "\n") {
		if !strings.HasPrefix(ln, "|") {
			continue
		}
		cells := strings.Split(ln, "|")
		if len(cells) < 5 {
			continue
		}
		title := strings.TrimSpace(cells[1])
		if !strings.Contains(title, "**") {
			continue // 表头/分隔行
		}
		out = append(out, row{lineno: i + 1, title: title, status: strings.TrimSpace(cells[2]), detail: strings.Join(cells[3:], "|")})
	}
	return out
}

// extractRefs 行文本→去重引用令牌（wip/ 分支+裸 hash；日期/编号假阳由
// rev-parse 验真兜掉——不存在的 ref 不计入判定）。
func extractRefs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range branchRe.FindAllString(text, -1) {
		if !seen[t] {
			seen[t], out = true, append(out, t)
		}
	}
	for _, t := range hashRe.FindAllString(text, -1) {
		if !seen[t] {
			seen[t], out = true, append(out, t)
		}
	}
	return out
}

// titleKey 票名 grep 键——标题剥 ~~/** 装饰取首个标点/空白前缀，
// ≥6 rune 才启用（短键宽匹配=误报源，宁可不查）。
func titleKey(title string) string {
	t := struckRe.ReplaceAllString(title, "")
	t = strings.TrimLeft(t, "~* ")
	if i := titleCut.FindStringIndex(t); i != nil {
		t = t[:i[0]]
	}
	t = strings.TrimSpace(t)
	if len([]rune(t)) < 6 {
		return ""
	}
	return t
}

// verdict 判定：≥1 个引用可解析为 commit 且全部可解析引用均为 master 祖先
// → 返首个祖先 ref 作证据；无祖先则空串。第二返值=可解析引用数
// （0=无拓扑证据不核，合法在飞态不误报）。
func verdict(repo, master string, refs []string) (string, int) {
	resolved := 0
	firstAnc := ""
	for _, r := range refs {
		if !resolveable(repo, r) {
			continue
		}
		resolved++
		if isAncestor(repo, r, master) {
			if firstAnc == "" {
				firstAnc = r
			}
		} else {
			return "", resolved // 有一未并入=保守不判（混合引用歧义）
		}
	}
	return firstAnc, resolved
}

// resolveable ref→commit 存在性验真（分支试 refs/heads/<r>、refs/remotes/*/<r>）。
func resolveable(repo, ref string) bool {
	for _, cand := range refCandidates(ref) {
		if _, err := git(repo, "rev-parse", "--verify", "--quiet", cand+"^{commit}"); err == nil {
			return true
		}
	}
	return false
}

func isAncestor(repo, ref, master string) bool {
	for _, cand := range refCandidates(ref) {
		if _, err := git(repo, "rev-parse", "--verify", "--quiet", cand+"^{commit}"); err != nil {
			continue
		}
		if _, err := git(repo, "merge-base", "--is-ancestor", cand, master); err == nil {
			return true
		}
	}
	return false
}

// refCandidates wip/ 分支试 heads→各 remote 面；hash 直试本体。
func refCandidates(ref string) []string {
	if !strings.HasPrefix(ref, "wip/") {
		return []string{ref}
	}
	return []string{ref, "origin/" + ref, "nox/" + ref}
}

func git(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	return cmd.CombinedOutput()
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
