// digest 确定性消化器（D-076）：宿主 cron 每日一次的纯 Go 二进制。
// 读：PG 只读（autotrade_logs REJECTED/FAILED + alerts critical 增量）
// 写：docs/经验库/（条目正文追加重现行）+ docs/经验库/待归纳.md（✅ 消解行自动移入 归档/待归纳-YYYY-MM.md）+ git commit
// 不做：LLM 调用、根因推理、新建条目、改 frontmatter、碰执行路径
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	commitFlag := flag.Bool("commit", false, "写入后有变更时自动 git commit（add 仅 docs/经验库/）")
	flag.Parse()
	if err := run(*commitFlag); err != nil {
		log.Fatalf("digest: %v", err)
	}
}

// run 主流程：读水位 → 读 DB 增量 → 分类匹配 → 写条目/待归纳 → 更新水位 → 可选 git commit。
func run(commitFlag bool) error {
	// 路径解析：默认相对 tools/digest 上溯到项目根 docs/经验库/；可经 env 覆盖。
	kbDir := envOr("NOX_KB_DIR", "../../docs/经验库")
	statePath := filepath.Join(kbDir, ".digest_state")
	dsn := envOr("NOX_PG_DSN", "postgres://nox:nox@localhost:5433/nox")

	// 1. 读水位
	state, err := loadState(statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	log.Printf("digest: water mark autotrade_logs=%d alerts=%d", state.AutotradeLogsMaxID, state.AlertsMaxID)

	// 2. 连 PG 只读
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect pg: %w", err)
	}
	defer pool.Close()

	// 2.5 化石水位自愈（库迁移/重建后旧水位>max(id)=增量面永久空读——钳回实存上限重锚）
	if err := clampWatermark(ctx, pool, state); err != nil {
		return fmt.Errorf("clamp watermark: %w", err)
	}

	// 3. 读增量
	logs, err := readAutotradeLogs(ctx, pool, state.AutotradeLogsMaxID)
	if err != nil {
		return fmt.Errorf("read autotrade_logs: %w", err)
	}
	alerts, err := readAlerts(ctx, pool, state.AlertsMaxID)
	if err != nil {
		return fmt.Errorf("read alerts: %w", err)
	}
	log.Printf("digest: %d autotrade_logs + %d alerts since water mark", len(logs), len(alerts))

	// 4. 加载索引 + frontmatter（staleness）
	indexPath := filepath.Join(kbDir, "索引.md")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("read index: %w", err)
	}
	indexRows := parseIndex(string(indexData))
	fmEntries, err := loadFrontmatterEntries(filepath.Join(kbDir, "条目"))
	if err != nil {
		return fmt.Errorf("load frontmatter entries: %w", err)
	}

	// 5. 分类 + 匹配 + 写条目正文
	now := time.Now().UTC()
	dateStr := now.Format("2006-01-02")
	var pending []pendingEvent
	hits := 0
	// 返修包① T2：忽略表计数（风控正常触发类=零信号，不进待归纳不写条目）。
	ignored := map[string]int{}
	for _, l := range logs {
		ck := classifyAutotradeLog(l.Reason)
		// T2：忽略表（max positions/daily loss/state）→ 跳过并计数
		if isIgnored(ck) {
			ignored[ck]++
			continue
		}
		entryID, matched := matchEntry(ck, indexRows)
		if matched {
			// 追加到条目正文「## 证据」节
			entryPath := filepath.Join(kbDir, "条目", entryID+".md")
			content, err := os.ReadFile(entryPath)
			if err != nil {
				log.Printf("digest: read entry %s: %v", entryID, err)
				continue
			}
			summary := fmt.Sprintf("%s @ %s", ck, l.Canonical)
			ref := fmt.Sprintf("autotrade_logs#%d", l.ID)
			newContent, ok := appendEvidenceLine(string(content), dateStr, summary, ref)
			if !ok {
				// 无证据节 → 进待归纳
				pending = append(pending, pendingEvent{Date: dateStr, Source: "autotrade_logs", ClassKey: ck, Summary: summary, Count: 1})
				continue
			}
			if err := os.WriteFile(entryPath, []byte(newContent), 0644); err != nil {
				log.Printf("digest: write entry %s: %v", entryID, err)
				continue
			}
			hits++
		} else {
			pending = append(pending, pendingEvent{Date: dateStr, Source: "autotrade_logs", ClassKey: ck, Summary: truncateSummary(l.Reason, 60), Count: 1})
		}
	}
	for _, a := range alerts {
		ck := classifyAlert(a.Message)
		// T2：忽略表（max positions/daily loss/state）→ 跳过并计数
		if isIgnored(ck) {
			ignored[ck]++
			continue
		}
		entryID, matched := matchEntry(ck, indexRows)
		if matched {
			entryPath := filepath.Join(kbDir, "条目", entryID+".md")
			content, err := os.ReadFile(entryPath)
			if err != nil {
				log.Printf("digest: read entry %s: %v", entryID, err)
				continue
			}
			summary := ck
			ref := fmt.Sprintf("alerts#%d", a.ID)
			newContent, ok := appendEvidenceLine(string(content), dateStr, summary, ref)
			if !ok {
				pending = append(pending, pendingEvent{Date: dateStr, Source: "alerts", ClassKey: ck, Summary: summary, Count: 1})
				continue
			}
			if err := os.WriteFile(entryPath, []byte(newContent), 0644); err != nil {
				log.Printf("digest: write entry %s: %v", entryID, err)
				continue
			}
			hits++
		} else {
			pending = append(pending, pendingEvent{Date: dateStr, Source: "alerts", ClassKey: ck, Summary: truncateSummary(a.Message, 60), Count: 1})
		}
	}

	// 6. staleness 报告
	staleness := parseStaleness(fmEntries, now)

	// 7. 待归纳.md 覆写（含历史事件聚合 + ⚠ 重复标记）；✅ 消解行先移入归档（消解出口）
	pendingPath := filepath.Join(kbDir, "待归纳.md")
	historyEvents, resolved := parsePendingHistory(pendingPath)
	if err := appendResolvedArchive(kbDir, resolved, now); err != nil {
		return fmt.Errorf("archive resolved: %w", err)
	}
	pendingMd := renderPendingMd(pending, historyEvents, staleness)
	// D-341 S4：带内循环·真实费率轨段（独立函数追加；v_cycle_realrate 视图最近 10 行）。
	// D-350：spot-carry 两段（候选前三行+纸面活跃行）。
	spotRows, err := loadSpotCarryUniverse(ctx, pool)
	if err != nil {
		log.Printf("load spot carry universe: %v", err)
	}
	pendingMd += RenderSpotCarrySection(spotRows)
	paperRows, err := loadPaperSpotCarry(ctx, pool)
	if err != nil {
		log.Printf("load paper spot carry: %v", err)
	}
	pendingMd += RenderPaperSpotCarrySection(paperRows)

	cycleRows, err := loadCycleRealrate(ctx, pool)
	if err != nil {
		log.Printf("digest: cycle realrate load: %v (段跳过)", err)
	} else {
		pendingMd += RenderCycleRealrateSection(cycleRows)
	}
	// D-356：资金不眠覆盖率段（五源逐池快照+汇总；输出序列尾段追加——沿段序末尾追加惯例）。
	covRows, err := loadCapitalCoverage(ctx, pool)
	if err != nil {
		log.Printf("digest: capital coverage load: %v (段跳过)", err)
	} else {
		pendingMd += RenderCapitalCoverageSection(covRows)
	}
	// 死角日报（票面：daemon/facts/策略三面在跑零产出对拍——纯报告件）。
	corners, err := loadDeadCorners(ctx, pool)
	if err != nil {
		log.Printf("digest: deadcorner load: %v (段跳过)", err)
	} else {
		pendingMd += renderDeadCornerSection(corners)
	}
	// 裁决/探矿两队列浮面（D-521 续+daily 死产出投影——读败段各自跳过）。
	pendingMd += queuesSection(ctx, pool)
	if err := os.WriteFile(pendingPath, []byte(pendingMd), 0644); err != nil {
		return fmt.Errorf("write pending: %w", err)
	}

	// D-172 积压闸：未消解行（✅ 已在 parsePendingHistory 剔除）超阈 → InsertAlert warn。
	agg := aggregatePending(append(historyEvents, pending...))
	rows, maxCount := backlogStats(agg)
	backlogAlert(ctx, pool, rows, maxCount)
	if backlogExceeded(rows, maxCount) {
		log.Printf("digest: backlog gate fired rows=%d maxCount=%d", rows, maxCount)
	}

	// 消化链建制①：经济库待归纳积压闸（D-172 同机制——行>0 且最老>3天→warn）。
	econBacklogGate(ctx, pool, kbDir, now)

	// 8. 更新水位
	newState := &digestState{
		AutotradeLogsMaxID: state.AutotradeLogsMaxID,
		AlertsMaxID:        state.AlertsMaxID,
	}
	for _, l := range logs {
		if l.ID > newState.AutotradeLogsMaxID {
			newState.AutotradeLogsMaxID = l.ID
		}
	}
	for _, a := range alerts {
		if a.ID > newState.AlertsMaxID {
			newState.AlertsMaxID = a.ID
		}
	}
	if err := saveState(statePath, newState); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// 返修包① T2：忽略表汇总 log（风控正常触发类计数，如 "ignored 478 noise (state=475, max positions=2, daily loss=1)"）。
	totalIgnored := 0
	for _, n := range ignored {
		totalIgnored += n
	}
	if totalIgnored > 0 {
		var parts []string
		for _, k := range []string{"state", "max positions", "daily loss"} {
			if n := ignored[k]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", k, n))
			}
		}
		log.Printf("digest: ignored %d noise (%s)", totalIgnored, strings.Join(parts, ", "))
	}

	log.Printf("digest: %d hits / %d pending / %d resolved→archive / %d staleness → written", hits, len(pending), len(resolved), len(staleness))

	// H3：-commit flag → 有变更时自动 git commit（add 仅 docs/经验库/，repo 根执行）
	if commitFlag && (hits > 0 || len(pending) > 0 || len(resolved) > 0 || len(staleness) > 0) {
		if err := gitCommitKB(kbDir, dateStr, hits, len(pending)); err != nil {
			log.Printf("digest: git commit failed: %v", err)
		}
	}
	return nil
}

// gitCommitKB 在 repo 根执行 git add docs/经验库/ + git commit（H3）。
// kbDir 为 docs/经验库 绝对/相对路径；repo 根 = kbDir 上溯两级（docs/经验库 → repo 根）。
// 无变更时 git commit 退出非零 → 视为正常跳过（不报错）。
// 修复⑭ S7：kbDir 相对路径时 filepath.Rel 需两端同基——先 Abs(kbDir) 再 Rel。
func gitCommitKB(kbDir, dateStr string, hits, pending int) error {
	kbAbs, err := filepath.Abs(kbDir)
	if err != nil {
		return fmt.Errorf("abs kb dir: %w", err)
	}
	repoRoot := filepath.Join(kbAbs, "..", "..")
	kbRel, err := filepath.Rel(repoRoot, kbAbs)
	if err != nil {
		return fmt.Errorf("rel kb dir: %w", err)
	}
	// git add <kbRel>/
	addCmd := exec.Command("git", "-C", repoRoot, "add", "--", kbRel+"/")
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add %s: %v\n%s", kbRel, err, strings.TrimSpace(string(out)))
	}
	// 检查是否有 staged 变更（避免空 commit 报错）
	statusCmd := exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet")
	if err := statusCmd.Run(); err == nil {
		log.Printf("digest: git commit skipped (no staged changes)")
		return nil
	}
	msg := fmt.Sprintf("digest: %s %d 重现 / %d 待归纳", dateStr, hits, pending)
	commitCmd := exec.Command("git", "-C", repoRoot, "commit", "-m", msg)
	out, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	log.Printf("digest: git commit ok → %s", strings.TrimSpace(string(out)))
	return nil
}

// envOr 读 env，缺省返回 def。
// D-145 引导件：NOX_KB_DIR/NOX_PG_DSN=工具引导参数（连库前/产物路径），非业务配置。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
