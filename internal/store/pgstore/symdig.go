// symdig.go —— D-464：fin_symbol_aliases 读面 + fin_symdig_findings 写面。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// ListSymbolAliases alias→canonical 全表（D-464 S1；scan/symdig 一次载入传参不逐行查）。
func (s *Store) ListSymbolAliases(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT alias, canonical FROM fin_symbol_aliases`)
	if err != nil {
		return nil, fmt.Errorf("list symbol aliases: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var a, c string
		if err := rows.Scan(&a, &c); err != nil {
			return nil, err
		}
		out[a] = c
	}
	return out, rows.Err()
}

// SymdigFinding fin_symdig_findings 行（D-464 S2）。
type SymdigFinding struct {
	Venue, Symbol, Klass, Reason string
	Payload                      map[string]any
}

// InsertSymdigFindings 单轮 findings 批写（同 run_ts 由调用方统一）。
func (s *Store) InsertSymdigFindings(ctx context.Context, runTS time.Time, rows []SymdigFinding) error {
	for _, r := range rows {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO fin_symdig_findings (run_ts, venue, symbol, klass, reason, payload)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			runTS, r.Venue, r.Symbol, r.Klass, r.Reason, r.Payload); err != nil {
			return fmt.Errorf("insert symdig finding: %w", err)
		}
	}
	return nil
}

// LatestSymdigKeys 上一轮 run_ts 的 (venue|symbol|klass) 键集（diff 节流用；无历史=空集）。
func (s *Store) LatestSymdigKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, symbol, klass FROM fin_symdig_findings
		WHERE run_ts = (SELECT max(run_ts) FROM fin_symdig_findings)`)
	if err != nil {
		return nil, fmt.Errorf("latest symdig keys: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v, sy, k string
		if err := rows.Scan(&v, &sy, &k); err != nil {
			return nil, err
		}
		out[v+"|"+sy+"|"+k] = true
	}
	return out, rows.Err()
}

// ListCarryUniversePairs 当前 universe 快照的 (mt_venue|canonical) 键集（symdig 配对判定）。
func (s *Store) ListCarryUniversePairs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT mt_venue, canonical FROM fin_carry_universe`)
	if err != nil {
		return nil, fmt.Errorf("list carry universe pairs: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		out[v+"|"+c] = true
	}
	return out, rows.Err()
}
