// econ_backlog.go 经济经验库待归纳积压闸（消化链建制票①——D-172 同机制复用零新轮）：
// docs/经济经验库/待归纳.md 未裁定行 >0 且最老行 >econBacklogDays → InsertAlert warn。
// 行口径：`- ` 起且不带 ✅ 消解标（机械行 settled=YYYY-MM-DD 供年龄；[LLM草稿]
// 行无日期按 0 天计只计数）。库读败/文件缺=静默跳过（同 parsePendingHistory 先例）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// econBacklogDays 积压天数阈（票面「积压>N 天」——取 3 天：负结算教训三日无人裁=病灶）。
const econBacklogDays = 3

// econBacklogStats 解析 econ 待归纳文件 → 未裁定行数 + 最老 settled 天数。
func econBacklogStats(path string, now time.Time) (rows, oldestDays int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0 // 文件缺=零积压
	}
	oldest := -1
	for _, ln := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "- ") || strings.Contains(t, "✅") {
			continue
		}
		rows++
		if i := strings.Index(t, "settled="); i >= 0 {
			d := t[i+len("settled="):]
			if j := strings.IndexAny(d, " \t"); j >= 0 {
				d = d[:j]
			}
			if day, perr := time.Parse("2006-01-02", d); perr == nil {
				if age := int(now.Sub(day).Hours() / 24); age > oldest {
					oldest = age
				}
			}
		}
	}
	if oldest < 0 {
		oldest = 0
	}
	return rows, oldest
}

// econBacklogAlert 超阈直插 alerts warn（backlogAlert 同款直写——digest 不持 *Store）。
func econBacklogAlert(ctx context.Context, pool *pgxpool.Pool, rows, oldestDays int) {
	if rows <= 0 || oldestDays <= econBacklogDays {
		return
	}
	msg := fmt.Sprintf("经济经验库待归纳积压 %d 行（最老 %d 天 > 阈 %d 天——裁定三分出路 canon/dismiss/merge 未走）",
		rows, oldestDays, econBacklogDays)
	if _, err := pool.Exec(ctx, `INSERT INTO alerts (level, message) VALUES ($1, $2)`, "warn", msg); err != nil {
		log.Printf("digest: econ backlog alert insert: %v", err)
	}
}

// econBacklogGate 主调用面（main.go 单行挂接——棘轮 300 行面）：解析→超阈告警→计数 log。
func econBacklogGate(ctx context.Context, pool *pgxpool.Pool, kbDir string, now time.Time) {
	rows, oldest := econBacklogStats(filepath.Join(econDirPath(kbDir), "待归纳.md"), now)
	econBacklogAlert(ctx, pool, rows, oldest)
	if rows > 0 {
		log.Printf("digest: econ backlog rows=%d oldest=%dd", rows, oldest)
	}
}

// econDirPath 经济库目（env 覆写优先→kbDir 兄弟目回落——同 econkb.Dir 三序口径）。
func econDirPath(kbDir string) string {
	if d := os.Getenv("NOX_ECONKB_DIR"); d != "" {
		return d
	}
	return filepath.Join(kbDir, "..", "经济经验库")
}
