// Package pgstore 提供 PostgreSQL 存储实现（§4.2/§2）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 封装 pgx 连接池，所有数据库操作共享同一池。
type Store struct {
	pool        *pgxpool.Pool
	alertPaused bool // D-069：暂停全部告警写入（demo 阶段避免刷屏）
}

// SetAlertPaused 暂停/恢复告警写入（D-069）。
func (s *Store) SetAlertPaused(paused bool) {
	s.alertPaused = paused
}

// New 创建带连接池的 Store。
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
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
