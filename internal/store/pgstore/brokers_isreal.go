package pgstore

import "context"

// D-144 ① 三态：实盘/demo/未知 标记 store 方法（从 brokers.go 拆出控行数）。

// SetBrokerAccountIsReal 更新账号实盘标记。isReal=nil → 清除回未知(NULL)。
func (s *Store) SetBrokerAccountIsReal(ctx context.Context, venue string, isReal *bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET is_real = $1 WHERE venue = $2`, isReal, venue)
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

// ListVenueIsReal 返回统一 venue→*bool 映射（D-178）。
// 合并 broker_accounts.is_real（CFD）+ exchange_configs.simulated（CEX，取反）。
// nil=未知（broker_accounts.is_real=NULL 或 venue 不在任何表）。
// CEX 查询失败 → 仅返回 CFD 部分（fail-open，不阻塞检测）。
func (s *Store) ListVenueIsReal(ctx context.Context) (map[string]*bool, error) {
	m, err := s.ListAccountIsReal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT venue, simulated FROM exchange_configs`)
	if err != nil {
		return m, nil // CEX 查询失败 → 仅返回 CFD（fail-open）
	}
	defer rows.Close()
	for rows.Next() {
		var venue string
		var simulated bool
		if err := rows.Scan(&venue, &simulated); err != nil {
			continue
		}
		isReal := !simulated
		m[venue] = &isReal
	}
	return m, rows.Err()
}
