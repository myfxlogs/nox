// handoff.go 日报 §5 交接面：纯 Go 机械装配（零 AI）——决策席 boot 输入。
// 内容=STATE.md 在轨行摘要 + 阻塞/待决策块 + 收件箱积压行数 + 当日 git log。
// 全部 best-effort 降级：文件/git 不可读时留空段标注，不炸报告。
package sysretro

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"arb/internal/ir/inboxfmt"
)

// handoffBlock 装配交接面 fenced 块内容。
func handoffBlock(deps Deps, now time.Time) string {
	var b strings.Builder
	b.WriteString("```\n")
	b.WriteString("in_flight_tasks:\n")
	stateData, err := os.ReadFile(deps.StatePath)
	if err != nil {
		fmt.Fprintf(&b, "  (STATE.md 不可读: %v)\n", err)
	} else {
		b.WriteString(stateInFlight(string(stateData)))
	}
	fmt.Fprintf(&b, "blocked_line: %s\n", blockedLine(string(stateData)))
	fmt.Fprintf(&b, "inbox_backlog_rows: %d\n", inboxBacklog(deps.InboxPath))
	b.WriteString("commits_yesterday:\n")
	b.WriteString(gitLogOn(deps.RepoDir, now.AddDate(0, 0, -1).Format("2006-01-02")))
	b.WriteString("```")
	return b.String()
}

// stateInFlight 施工表在轨行摘要（🔄/📋 行 → `- D# 状态 owner` 压缩行）。
func stateInFlight(state string) string {
	var out strings.Builder
	for _, ln := range strings.Split(state, "\n") {
		if !strings.Contains(ln, "🔄") && !strings.Contains(ln, "📋") {
			continue
		}
		// 行形如 `||| **D-450 xxx** | 状态...`——抽任务名。
		name := ln
		if i := strings.Index(ln, "**"); i >= 0 {
			if j := strings.Index(ln[i+2:], "**"); j >= 0 {
				name = strings.TrimSpace(ln[i+2 : i+2+j])
			}
		}
		fmt.Fprintf(&out, "  - %s\n", name)
	}
	if out.Len() == 0 {
		return "  (none)\n"
	}
	return out.String()
}

// blockedLine 交接负载「阻塞/待决策」行原文。
func blockedLine(state string) string {
	for _, ln := range strings.Split(state, "\n") {
		if strings.Contains(ln, "阻塞") && strings.Contains(ln, "待决策") {
			return strings.TrimSpace(strings.TrimPrefix(ln, "-"))
		}
	}
	return "(none)"
}

// inboxBacklog 收件箱未分诊行数（D-534 语义改未分诊=pending∪旧无 status 行；实现= inboxfmt.Backlog）。
func inboxBacklog(inboxPath string) int { return inboxfmt.Backlog(inboxPath) }

// gitLogOn 指定北京历日当日 commit 列表（D-510 S3：日界 +08:00）；git 不可用/非仓库 → 降级标注。
func gitLogOn(repoDir, day string) string {
	out, err := exec.Command("git", "-C", repoDir, "log",
		"--since="+day+"T00:00:00+08:00", "--until="+day+"T23:59:59+08:00", "--oneline", "--all").Output()
	if err != nil {
		return "  (git log 不可用)\n"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return "  (none)\n"
	}
	if len(lines) > 20 {
		lines = lines[:20]
	}
	var b strings.Builder
	for _, ln := range lines {
		fmt.Fprintf(&b, "  %s\n", ln)
	}
	return b.String()
}
