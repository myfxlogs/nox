package pgstore

import (
	"context"
	"fmt"

	"arb/internal/keydomain"
)

// D-144 ① 三态：实盘/demo/未知 标记 store 方法（从 brokers.go 拆出控行数）。

// SetBrokerAccountIsReal 更新账号实盘标记。isReal=nil → 清除回未知(NULL)。
func (s *Store) SetBrokerAccountIsReal(ctx context.Context, venue string, isReal *bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET is_real = $1 WHERE venue = $2`, isReal, venue)
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("%w: %s", ErrBrokerAccountNotFound, venue)
	}
	return err
}

// ListAccountIsReal 返回 venue→*bool 映射（D-144 ① 三态：nil=未知，discoverSwapCombos 同类型配对用）。
func (s *Store) ListAccountIsReal(ctx context.Context) (map[string]*bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT venue, is_real FROM broker_accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]*bool)
	for rows.Next() {
		var venue string
		var isReal *bool
		if err := rows.Scan(&venue, &isReal); err != nil {
			return nil, err
		}
		m[venue] = isReal
	}
	return m, rows.Err()
}

// ListVenueIsReal 返回统一 venue:type → *bool 映射（D-178；D-371 键域升级 venueKey 粒度）。
// 合并 broker_accounts.is_real（MT：key=venue+":"+platform，0→mt4/1→mt5——brokers.go:15
// 注释 × MT4Executor.venueKey=venue+":mt4" 互证）+ exchange_configs.simulated 取反
// （CEX：DISTINCT ON (venue,venue_type) 赢家 config——created_at DESC, id DESC，与
// reconcile.CexKeyWinners 同语义镜像【注释互指防漂移】；pgstore 不能 import reconcile=环，
// SQL 单查询是 pgstore 内最简等价，配平锚 D-371 S5-e 钉双实现一致性）。
// nil=未知（is_real=NULL 或 venue 不在任何表）。两半边独立容错（CEX 查询失败仍返回 MT 半边）。
func (s *Store) ListVenueIsReal(ctx context.Context) (map[string]*bool, error) {
	m := make(map[string]*bool)
	// MT 半边：broker_accounts（key=venue:platform 映射）
	mtRows, err := s.pool.Query(ctx, `SELECT venue, venue_type, is_real FROM broker_accounts`)
	if err != nil {
		return nil, err
	}
	defer mtRows.Close()
	for mtRows.Next() {
		var venue, vt string
		var isReal *bool
		if err := mtRows.Scan(&venue, &vt, &isReal); err != nil {
			return nil, err
		}
		m[keydomain.New(venue, vt).String()] = isReal
	}
	if err := mtRows.Err(); err != nil {
		return nil, err
	}
	// CEX 半边：exchange_configs 赢家 config（撞键取 created_at/id 最新）
	cexRows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, venue_type) venue, venue_type, simulated
		FROM exchange_configs
		ORDER BY venue, venue_type, created_at DESC, id DESC`)
	if err != nil {
		return m, nil // CEX 查询失败 → 仅返回 MT 半边（fail-open）
	}
	defer cexRows.Close()
	for cexRows.Next() {
		var venue, vt string
		var simulated bool
		if err := cexRows.Scan(&venue, &vt, &simulated); err != nil {
			continue
		}
		isReal := !simulated
		m[venue+":"+vt] = &isReal
	}
	return m, cexRows.Err()
}

// BrokerRealAccountKeys D-360 S2：real 账户键集（venue=user_id::text，核证点②：
// 与 fin_venue_account_snapshot.venue 等值，生产 DB 两列互证留证 9-20）。
// 快照写时过滤用（证真才留）；查询失败由调用方 fail-closed 处置。
func (s *Store) BrokerRealAccountKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT venue FROM broker_accounts WHERE is_real`)
	if err != nil {
		return nil, fmt.Errorf("broker real account keys: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}
