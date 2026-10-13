// induct.go —— 经验库自动归纳管线（业主令 10-13 STATE 行）。
// 段：待归纳*.md+分析员 pending 面 → 关键词分诊(covered:<id>) → LLM 聚类草稿(条目/<slug>.md,status:observed)
// → gen 重产索引 → 源行打 inducted:<hash> 标幂等。
// 禁线：只写 docs/（fin_llm_call 账本行为唯一库写，由 analyst.CallLog 经 pgstore 落）；
// LLM 未配置/违式 → fail-open 跳过草稿段（确定性 covered 标仍落）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/review/kbreport"
	"nox/internal/store/pgstore"
)

var (
	pendingFile = "../../docs/经验库/待归纳.md"
	inboxFile   = "../../docs/经验库/待归纳-分析员.md"
)

// applyKBDirEnv NOX_KB_DIR 工具引导参数覆写全部路径基准（同 digest 先例，非业务配置）。
func applyKBDirEnv() {
	v := os.Getenv("NOX_KB_DIR")
	if v == "" {
		return
	}
	entriesDir = filepath.Join(v, "条目")
	indexFile = filepath.Join(v, "索引.md")
	skillsIndexFile = filepath.Join(v, "技能清单.md")
	pendingFile = filepath.Join(v, "待归纳.md")
	inboxFile = filepath.Join(v, "待归纳-分析员.md")
}

// inductCmd 命令入口：ctx + LLM 装配（fail-open）→ induct 主管线。
func inductCmd() error {
	ctx := context.Background()
	runner, closer := resolveInductLLM(ctx)
	defer closer()
	_, _, err := induct(ctx, inductOpts{llm: runner})
	return err
}

// inductOpts 可注入依赖（测试桩）。
type inductOpts struct {
	now time.Time
	llm llmRunner // nil=LLM 段跳过
}

// resolveInductLLM 连 PG→DB AI 配置解析→CallLog 挂 fin_llm_call 账本（role=kb_induct）。
// 任一环节失败 → runner=nil（fail-open：LLM 段跳过，covered 标不受影响）。
// 成功时返回 runner + closer（st 生命周期随调用方——CallLog 闭包持有 st，禁提前关）。
func resolveInductLLM(ctx context.Context) (llmRunner, func()) {
	dsn := os.Getenv("NOX_PG_DSN")
	if dsn == "" {
		dsn = "postgres://nox:nox@localhost:5433/nox"
	}
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		log.Printf("induct: pgstore %v——LLM 段跳过", err)
		return nil, func() {}
	}
	// AI 配置只走 DB（ai_settings）——NOX_AI_* 属 app_secrets 域，env 直读门禁禁读；
	// env 三参置空（ResolveClient env 回落留空=DB 单源）。
	c, err := kbreport.ResolveClient(ctx, st, "", "", "")
	if err != nil {
		log.Printf("induct: resolve client %v——LLM 段跳过", err)
		st.Close()
		return nil, func() {}
	}
	c.CallLog = analyst.LLMLogger(st, "kb_induct")
	return defaultLLMRunner(c), func() { st.Close() }
}

// induct 主管线。返回统计（covered/drafted/skipped）。
func induct(ctx context.Context, o inductOpts) (int, int, error) {
	if o.now.IsZero() {
		o.now = time.Now().UTC()
	}
	date := o.now.Format("2006-01-02")

	// 1. 解析两源文件未消解行
	pend, pendLines, err := parsePendingFile(pendingFile)
	if err != nil {
		return 0, 0, err
	}
	inbox, inboxLines, err := parseInboxFile(inboxFile)
	if err != nil {
		log.Printf("induct: inbox %v——该源跳过", err)
		inbox = nil
	}
	all := append(pend, inbox...)
	if len(all) == 0 {
		log.Printf("induct: 零待归纳行")
		return 0, 0, nil
	}
	log.Printf("induct: %d pending + %d inbox 未消解行", len(pend), len(inbox))

	// 2. 关键词分诊：命中既有条目 → covered:<id> 标
	entries, err := loadEntries(entriesDir)
	if err != nil {
		return 0, 0, fmt.Errorf("load entries: %w", err)
	}
	pendMarks, inboxMarks := map[int]string{}, map[int]string{}
	var remain []pendingRow
	covered := 0
	for _, r := range all {
		h := rowHash(r)
		if id := matchCovered(r.Summary, entries); id != "" {
			if r.Source == "analyst-inbox" {
				inboxMarks[r.Line] = " inducted:" + h + " covered:" + id
			} else {
				pendMarks[r.Line] = " ✅ inducted:" + h + " covered:" + id
			}
			covered++
			continue
		}
		remain = append(remain, r)
	}

	// 3. LLM 聚类→草稿（违式/未配 fail-open）
	drafted := 0
	if o.llm != nil && len(remain) > 0 {
		drafted = draftSegment(ctx, o, remain, entries, pendMarks, inboxMarks, date)
	} else if len(remain) > 0 {
		log.Printf("induct: LLM 未配——%d 行留待下次", len(remain))
	}

	// 4. 源标写回（行级原位，digest 未跑前重跑零重复）
	if len(pendMarks) > 0 {
		if err := os.WriteFile(pendingFile, []byte(joinLines(markRows(pendLines, pendMarks))), 0o644); err != nil {
			return covered, drafted, fmt.Errorf("write pending: %w", err)
		}
	}
	if len(inboxMarks) > 0 {
		if err := os.WriteFile(inboxFile, []byte(joinLines(markRows(inboxLines, inboxMarks))), 0o644); err != nil {
			return covered, drafted, fmt.Errorf("write inbox: %w", err)
		}
	}

	// 5. 索引重产
	if drafted > 0 || covered > 0 {
		if err := gen(); err != nil {
			return covered, drafted, fmt.Errorf("gen: %w", err)
		}
	}
	log.Printf("induct: covered=%d drafted=%d remain=%d", covered, drafted, len(remain))
	return covered, drafted, nil
}

// draftSegment LLM 聚类段：一次调用产草稿→schema 闸→落条目+源行标。
func draftSegment(ctx context.Context, o inductOpts, remain []pendingRow, entries []*Entry,
	pendMarks, inboxMarks map[int]string, date string) int {
	text, err := o.llm(ctx, inductSystem, inductPrompt(remain))
	if err != nil {
		log.Printf("induct: llm %v——草稿段跳过", err)
		return 0
	}
	existing := map[string]bool{}
	for _, e := range entries {
		existing[e.ID] = true
	}
	drafts, err := parseDrafts(text, existing)
	if err != nil {
		log.Printf("induct: schema 闸 %v——整批不落盘", err)
		return 0
	}
	n := 0
	for _, d := range drafts {
		path := filepath.Join(entriesDir, d.ID+".md")
		if _, err := os.Stat(path); err == nil {
			log.Printf("induct: %s 文件已存在跳过", d.ID)
			continue
		}
		if err := os.WriteFile(path, []byte(renderDraft(d, remain, date)), 0o644); err != nil {
			log.Printf("induct: write %s: %v", d.ID, err)
			continue
		}
		for _, i := range d.Rows {
			if i < 0 || i >= len(remain) {
				continue
			}
			r := remain[i]
			h := rowHash(r)
			if r.Source == "analyst-inbox" {
				inboxMarks[r.Line] = " inducted:" + h + " draft:" + d.ID
			} else {
				pendMarks[r.Line] = " ✅ inducted:" + h + " draft:" + d.ID
			}
		}
		n++
	}
	return n
}

func joinLines(ls []string) string { return strings.Join(ls, "\n") }
