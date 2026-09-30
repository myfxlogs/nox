// alerts.go —— alerts 表函数族（D-411 自 opportunities.go 拆出：>300 行门禁）。
// Alert/InsertAlert/InsertPositionAlert/ListAlerts/AckAlert/AckAll/PurgeAckedAlerts/
// DeleteAlerts/AlertExistsByFragment/ClearAckedAlerts/ListUnacked。
package pgstore

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/alerttext"
)

// Alert 对应 alerts 表（§2.5）。
type Alert struct {
	ID     int64
	RuleID *int64
	// D-411：position_id 持仓级告警关联（NULL=非持仓告警；与 rule_id 两语义分列）。
	PositionID *int64
	RuleName   string
	Ts         time.Time
	Level      string
	Message    string
	Acked      bool
}

// InsertAlert 写入告警（§2.5）。
// D-069：alertPaused=true 时静默丢弃（demo 阶段避免刷屏）。
// D-114：写入成功后回调 alertHook（main.go 注入 alertTopic.Publish → StreamUpdates case 3 实时推送）。
// 用 INSERT...RETURNING 取回完整 Alert 对象（含 id/ts），回调传完整对象。
func (s *Store) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	return s.insertAlert(ctx, ruleID, nil, level, message)
}

// InsertPositionAlert D-411 S1：持仓级告警——写 position_id（rule_id=NULL）。
// 历史缺陷：持仓域调用方曾把 posID 塞进 rule_id（FK→rules 违规静默丢告警，
// 生产 981 行 rule_id 全 NULL、498 残腿 critical 从未落表）。两语义分函数，
// 调用方编译期选对。
func (s *Store) InsertPositionAlert(ctx context.Context, posID int64, level, message string) error {
	return s.insertAlert(ctx, nil, &posID, level, message)
}

// insertAlert 共享插入实现（D-411：列差异参数化，禁第二份插入逻辑）。
func (s *Store) insertAlert(ctx context.Context, ruleID, posID *int64, level, message string) error {
	message = alerttext.VenueErrCN(message) // D-588：venue 英文错误壳中文化（漏斗单点=表+topic+feishu 三通道同归一）
	if s.alertPaused {
		return nil
	}
	// D-592 S1：同键窗口抑制（DB 实查非进程内存——cron 批处理进程态活不过一轮，
	// KB cron-monitor-state-persistence）；漏斗单点=不插行即同抑 topic/feishu 通道。
	// fail-open：dedup 查询出错放行原路径（止血票不引新丢告警面）。
	w := s.alertDedupWindow
	if w == 0 {
		w = defaultAlertDedupWindow
	}
	key := alerttext.AlertDedupKey(level, message)
	dup, derr := s.recentAlertDuplicate(ctx, key, level, time.Now().Add(-w))
	if derr != nil {
		log.Printf("[pgstore] alert dedup query fail (fail-open): %v", derr)
	} else if dup {
		return nil // 同键窗内：不插行、不推 feishu/topic
	}
	// D-496：rules/rule_id 已删——ruleID 参数保留签名兼容、恒不落库。
	q := `INSERT INTO alerts (position_id, level, message) VALUES ($1, $2, $3)
		RETURNING id, ts, level, message, acked`
	var a Alert
	a.RuleID = ruleID
	a.PositionID = posID
	err := s.pool.QueryRow(ctx, q, posID, level, message).Scan(&a.ID, &a.Ts, &a.Level, &a.Message, &a.Acked)
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	// D-114：写入后回调（main.go 注入 alertTopic.Publish → StreamUpdates case 3 实时推送）。
	// fire-and-forget：回调失败不影响写入（同 oppTopic 模式）。nil = 未接线（测试）。
	if s.alertHook != nil {
		s.alertHook(a)
	}
	return nil
}

// defaultAlertDedupWindow 同键去重窗缺省（D-412 F4 形态：常量缺省+env 装配覆盖）。
const defaultAlertDedupWindow = 30 * time.Minute

// recentAlertDuplicate 窗口内同键实查（候选按 level 收窄；归一键不落库=零 schema
// 变更，Go 侧对存量行重算键比对）。
func (s *Store) recentAlertDuplicate(ctx context.Context, key, level string, cutoff time.Time) (bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT message FROM alerts WHERE ts > $1 AND level=$2`, cutoff, level)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return false, err
		}
		if alerttext.AlertDedupKey(level, msg) == key {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ListAlerts 分页返回告警（§8.2）。
func (s *Store) ListAlerts(ctx context.Context, limit, offset int) ([]Alert, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT a.id, NULL::bigint, a.position_id, '', a.ts, a.level, a.message, a.acked
		FROM alerts a
		ORDER BY a.ts DESC LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.RuleID, &a.PositionID, &a.RuleName, &a.Ts, &a.Level, &a.Message, &a.Acked); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AckAlert 确认单条告警（幂等）。
func (s *Store) AckAlert(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE alerts SET acked = true WHERE id = $1`, id)
	return err
}

// AckAll 确认全部未读告警。
func (s *Store) AckAll(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE alerts SET acked = true WHERE acked = false`)
	return err
}

// PurgeAckedAlerts 删除已确认且超期（默认 7 天）的告警（P5 修复⑧ TTL）。
// 幂等：每次调用执行同一 SQL，删除符合条件的行；未 acked 不删（待办语义）。
// 返回删除行数（用于日志/验收）。
func (s *Store) PurgeAckedAlerts(ctx context.Context, maxAge time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alerts WHERE acked = true AND ts < now() - $1::interval`, maxAge.String())
	if err != nil {
		return 0, fmt.Errorf("purge acked alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteAlerts 批量删除指定 ID 的告警（不管 acked 状态）。
func (s *Store) DeleteAlerts(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM alerts WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("delete alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AlertExistsByFragment 检查告警历史中是否已存在含 fragment 的行（D-265 事件盯梢去重）。
// fragment = 公告唯一标识（FileLink 或 URL），用于 LIKE 匹配。
// D-508 S3：LIKE 加 7d 时界——全表扫历史告警随体量退化，去重窗口 7d 足够盖公告重推周期。
func (s *Store) AlertExistsByFragment(ctx context.Context, fragment string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '%' || $1 || '%' AND ts > now() - interval '7 days'`,
		fragment).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("alert exists check: %w", err)
	}
	return count > 0, nil
}

// ClearAckedAlerts 删除所有已确认告警（不管时间）。
func (s *Store) ClearAckedAlerts(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alerts WHERE acked = true`)
	if err != nil {
		return 0, fmt.Errorf("clear acked alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListUnacked 返回未读告警 + 计数。
func (s *Store) ListUnacked(ctx context.Context) ([]Alert, int, error) {
	q := `SELECT a.id, NULL::bigint, a.position_id, '', a.ts, a.level, a.message, a.acked
		FROM alerts a
		WHERE a.acked = false ORDER BY a.ts DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, 0, fmt.Errorf("list unacked: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.RuleID, &a.PositionID, &a.RuleName, &a.Ts, &a.Level, &a.Message, &a.Acked); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, len(out), nil
}
