// econkb.go —— D-587 经济经验库产线接线：负结算自动产待归纳行。
// position_settlements 落 net_realized<0 且 incident_attributed=false 行时
// （事故行走事故复盘径非策略坑不产），机械 append docs/经济经验库/待归纳.md，
// settlement_event_key 去重幂等（同 key 重跑零重复）。
// 目录经 NOX_ECONKB_DIR 覆写（工具引导级非业务配置——测试重指 tmpdir）。
package pgstore

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const econPendingName = "待归纳.md"

func econKBDir() string {
	if d := os.Getenv("NOX_ECONKB_DIR"); d != "" {
		return d
	}
	// 容器面探针（D-587 返修选型裁定——探针优先于 compose env 补键）：
	// ./docs:/kn/docs 挂载在 → 结算写穿透落宿主（compose.yml:43）；
	// 宿主直跑（cron/工具）回落相对路径仓库根。
	if st, err := os.Stat("/kn/docs"); err == nil && st.IsDir() {
		return "/kn/docs/经济经验库"
	}
	return "docs/经济经验库"
}

// econPendingHeader 待归纳文件头（两产线口径钉死：机械行无前缀；LLM 起
// 草稿行必带 [LLM草稿] 前缀——草稿永不直接入库/永不改判据）。
const econPendingHeader = `# 经济经验库·待归纳（机械产线+LLM 草稿层）

- 机械行（settlement_event_key 幂等）：pos=%d net=%.2f settled=%s key=%s
- LLM 草稿行：以 [LLM草稿] 前缀开头——仅起草面，复审裁定转正方入 条目/
`

// econPendingAppend 幂等 append：event_key 已有行则跳（重跑零重复）。
func econPendingAppend(dir, key, line string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f := filepath.Join(dir, econPendingName)
	b, err := os.ReadFile(f)
	if err == nil && strings.Contains(string(b), "key="+key) {
		return nil // 幂等去重
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var w strings.Builder
	if err != nil {
		w.WriteString(econPendingHeader)
	}
	w.WriteString(line + "\n")
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fh.WriteString(w.String())
	return err
}

// econPendingForSettlement 负结算接线（InsertSettlement 赢家后调）：net<0
// 且 incident_attributed=false → 产机械待归纳行。best-effort——写文件败
// 只 log 不阻塞结算（账本已落，归纳面可补）。
func (s *Store) econPendingForSettlement(ctx context.Context, posID int64) {
	var net *float64
	var incident bool
	var key string
	if err := s.pool.QueryRow(ctx, `SELECT net_realized_usd, incident_attributed,
		COALESCE(settlement_event_key,'') FROM position_settlements WHERE position_id=$1`, posID).
		Scan(&net, &incident, &key); err != nil || net == nil || incident || key == "" || *net >= 0 {
		return
	}
	line := fmt.Sprintf("- pos=%d net=%.2f settled=%s key=%s",
		posID, *net, time.Now().UTC().Format("2006-01-02"), key)
	if err := econPendingAppend(econKBDir(), key, line); err != nil {
		log.Printf("[econkb] 待归纳 append 败 pos=%d: %v", posID, err)
	}
}
