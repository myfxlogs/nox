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
	// D-114：告警写入后回调（main.go 注入 alertTopic.Publish 触发实时推送）。
	// nil = 未接线（测试直接构造 Store）→ 不回调，InsertAlert 仍正常写 DB。
	alertHook func(a Alert)
}

// SetAlertPaused 暂停/恢复告警写入（D-069）。
func (s *Store) SetAlertPaused(paused bool) {
	s.alertPaused = paused
}

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
