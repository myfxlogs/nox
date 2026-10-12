// Package pgstore 提供 PostgreSQL 存储实现（§4.2/§2）。
package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 封装 pgx 连接池，所有数据库操作共享同一池。
type Store struct {
	pool        *pgxpool.Pool
	alertPaused bool // D-069：暂停全部告警写入（demo 阶段避免刷屏）
	// 同键去重窗（0=缺省 30min；env NOX_ALERT_DEDUP_WINDOW 装配层解析注入——D-412 F4 形态）。
	alertDedupWindow time.Duration
	// 告警写入后回调（main.go 注入 alertTopic.Publish 触发实时推送）。
	// nil = 未接线（测试直接构造 Store）→ 不回调，InsertAlert 仍正常写 DB。
	alertHook func(a Alert)
	// venue 账户快照落库后钩子（marginGuard 事件驱动评估挂点）。
	// nil = 未接线 → 不回调，InsertVenueAccountSnapshots 仍正常写 DB。
	snapHook func(ctx context.Context, snaps []VenueAccountSnapshot)
	// OnPositionChange D-469b S3：positions 表状态迁移后回调（写路径成功后、事务提交后
	// 触发，失败路径不发；nil=未接线）。消费方=position/personal monitor Kick。
	OnPositionChange func()
}

// SetAlertPaused 暂停/恢复告警写入（D-069）。
func (s *Store) SetAlertPaused(paused bool) {
	s.alertPaused = paused
}

// SetAlertDedupWindow D-592 S1：同键去重窗口覆盖（0=恢复缺省 30min）。
func (s *Store) SetAlertDedupWindow(d time.Duration) { s.alertDedupWindow = d }

// SetAlertHook 注入告警写入后回调（D-114：main.go 注入 alertTopic.Publish）。
func (s *Store) SetAlertHook(fn func(a Alert)) { s.alertHook = fn }

// New 创建带连接池的 Store。
// DSN 可通过 pool_max_conns / pool_min_conns 参数覆盖默认池大小
// （CI 集成测试用小池降低 fd 压力，D-177 race detector EBADF 根因修复）。
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	// 仅在 DSN 未显式指定时设默认值（pgxpool 默认 MinConns=0 无法区分「未设」与「显式设 0」，
	// 故用字符串检测）。
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = 10
	}
	if !strings.Contains(dsn, "pool_min_conns") {
		cfg.MinConns = 1
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close 关闭连接池。
func (s *Store) Close() {
	s.pool.Close()
}

// Pool 暴露底层连接池（供需要直接操作的场景使用）。
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// notifyPositionChange D-469b S3：持仓变更回调（nil 安全；调用方保证在写成功路径尾部调用）。
func (s *Store) notifyPositionChange() {
	if s.OnPositionChange != nil {
		s.OnPositionChange()
	}
}
