// explorer.go —— D-513 探矿员壳只读查询层（设计稿 v1.2 §2 ①⑤ + §4 state/proposals 读写）。
// 口径自证（KB query-selector-artifact-misread）：「最新值」= DISTINCT ON (kind,venue,canonical)
// ORDER BY ts DESC 取每组 ts 最大行——非 ORDER BY value 伪极值；离散度（min/max/极差/标准差）
// 在 Go 侧对行集确定性计算，SQL 不做聚合排序陷阱。
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// explorerKinds ①期限库离散度取数 kind 闭集（swap_long/short=MT 掉期，funding=CEX perp 资金费）。
var explorerKinds = []string{"swap_long", "swap_short", "funding"}

// ExplorerDispersion ①单 (kind, canonical) 跨 venue 离散度 + 7d 均值差。
type ExplorerDispersion struct {
	Kind      string
	Canonical string
	Venues    int       // 参与 venue 数（=1 时 Range/StdDev 恒 0，行仍保留供 LLM 见覆盖）
	Min       float64   // 跨 venue 最小最新值
	Max       float64   // 跨 venue 最大最新值
	Range     float64   // Max-Min 极差
	Mean      float64   // 跨 venue 均值
	StdDev    float64   // 样本标准差（venues<2 → 0）
	PrevMean  float64   // 7d 前时点各 venue 最新值均值（无 7d 前值 → NaN 不可序列化，用 HasPrev 标记）
	HasPrev   bool      // PrevMean 有效否
	Delta     float64   // Mean-PrevMean（HasPrev=false → 0）
	MinVenue  string    // 极差下限所在 venue（自证锚，非伪极值——由行集算）
	MaxVenue  string    // 极差上限所在 venue
	LatestTs  time.Time // 组内最旧 venue 的 ts（覆盖新鲜度下限）
}

// ExplorerTermDispersion ①期限库异常：三类 kind 最新值按 canonical 跨 venue 离散度+7d 差。
// canonical 解析序：MT 腿→broker_symbol_map.canonical（is_primary 优先）；CEX→fin_cex_universe.base
// 大写；皆无→裸 symbol 兜底（诚实键，不猜）。
func (s *Store) ExplorerTermDispersion(ctx context.Context, now time.Time) ([]ExplorerDispersion, error) {
	nowRows, err := s.explorerLatestFacts(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	prevRows, err := s.explorerLatestFacts(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		return nil, err
	}
	type key struct{ kind, canonical string }
	type acc struct {
		d    *ExplorerDispersion
		sum  float64
		sums float64
	}
	groups := map[key]*acc{}
	for _, r := range nowRows {
		k := key{r.Kind, r.Canonical}
		a := groups[k]
		if a == nil {
			d := &ExplorerDispersion{Kind: r.Kind, Canonical: r.Canonical,
				Min: r.Value, Max: r.Value, MinVenue: r.Venue, MaxVenue: r.Venue, LatestTs: r.Ts}
			a = &acc{d: d}
			groups[k] = a
		}
		a.d.Venues++
		a.sum += r.Value
		a.sums += r.Value * r.Value
		if r.Value < a.d.Min {
			a.d.Min, a.d.MinVenue = r.Value, r.Venue
		}
		if r.Value > a.d.Max {
			a.d.Max, a.d.MaxVenue = r.Value, r.Venue
		}
		if r.Ts.Before(a.d.LatestTs) {
			a.d.LatestTs = r.Ts
		}
	}
	prevSum := map[key]struct {
		sum float64
		n   int
	}{}
	for _, r := range prevRows {
		k := key{r.Kind, r.Canonical}
		p := prevSum[k]
		p.sum += r.Value
		p.n++
		prevSum[k] = p
	}
	out := make([]ExplorerDispersion, 0, len(groups))
	for k, a := range groups {
		d := a.d
		d.Mean = a.sum / float64(d.Venues)
		if d.Venues > 1 {
			variance := a.sums/float64(d.Venues) - d.Mean*d.Mean
			if variance > 0 {
				d.StdDev = math.Sqrt(variance * float64(d.Venues) / float64(d.Venues-1))
			}
		}
		d.Range = d.Max - d.Min
		if p, ok := prevSum[k]; ok && p.n > 0 {
			d.PrevMean, d.HasPrev = p.sum/float64(p.n), true
			d.Delta = d.Mean - d.PrevMean
		}
		out = append(out, *d)
	}
	return out, nil
}

// explorerFactRow ①原始行（每 kind×venue×canonical 一最新值）。
type explorerFactRow struct {
	Kind, Venue, Symbol, Canonical string
	Value                          float64
	Ts                             time.Time
}

// explorerLatestFacts before 为零值=当前最新；非零=该时点前最新（7d 前值口径）。
// DISTINCT ON 排序键与 distinct 键一致+ts DESC——最新值语义，禁改 value 排序（伪极值）。
func (s *Store) explorerLatestFacts(ctx context.Context, before time.Time) ([]explorerFactRow, error) {
	tsPred := ""
	args := []any{explorerKinds}
	if !before.IsZero() {
		tsPred = "AND f.ts <= $2"
		args = append(args, before)
	}
	q := `SELECT DISTINCT ON (f.kind, f.venue, COALESCE(NULLIF(m.canonical,''), NULLIF(upper(u.base),''), f.symbol))
		f.kind, f.venue, f.symbol,
		COALESCE(NULLIF(m.canonical,''), NULLIF(upper(u.base),''), f.symbol) AS canonical,
		f.value, f.ts
	FROM facts f
	LEFT JOIN broker_symbol_map m
		ON m.venue = f.venue AND m.broker_symbol = f.symbol AND m.canonical <> ''
	LEFT JOIN fin_cex_universe u
		ON u.venue = f.venue AND u.symbol = f.symbol
	WHERE f.kind = ANY($1) AND f.symbol IS NOT NULL ` + tsPred + `
	ORDER BY f.kind, f.venue,
		COALESCE(NULLIF(m.canonical,''), NULLIF(upper(u.base),''), f.symbol),
		f.ts DESC, m.is_primary DESC NULLS LAST`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("explorer latest facts: %w", err)
	}
	defer rows.Close()
	var out []explorerFactRow
	for rows.Next() {
		var r explorerFactRow
		if err := rows.Scan(&r.Kind, &r.Venue, &r.Symbol, &r.Canonical, &r.Value, &r.Ts); err != nil {
			return nil, fmt.Errorf("scan explorer facts: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ExplorerIdlePool ⑤单 venue 池：最新快照余额 + 近30d收入 + 闲置/破地板标记。
type ExplorerIdlePool struct {
	Venue          string
	VenueType      string
	Balance        float64
	Equity         float64
	FreeMargin     float64
	Currency       string
	BufferMultiple float64 // 快照行直取，NULL → 0
	Income30dUSD   float64 // income_events 近30d amount_usd 合计（venue_key/venue 双键兼容）
	FetchedAt      time.Time
	Idle           bool // 余额>0 且 30d 收入=0（睡钱标记，数字口径确定性）
	BelowFloor     bool // BufferMultiple 有值且 <1（破地板）
}

// ExplorerIdleFunds ⑤资金不眠异常：fin_venue_account_snapshot 每 venue 最新行 +
// income_events 近30d 合计 + 快照 buffer_multiple，闲置/破地板标记 Go 算。
func (s *Store) ExplorerIdleFunds(ctx context.Context, now time.Time) ([]ExplorerIdlePool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, venue_type)
			venue, venue_type, COALESCE(balance_raw,0), COALESCE(equity_raw,0),
			COALESCE(free_margin_raw,0), currency, fetched_at, COALESCE(buffer_multiple,0)
		FROM fin_venue_account_snapshot
		ORDER BY venue, venue_type, fetched_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("explorer idle snapshots: %w", err)
	}
	defer rows.Close()
	var pools []ExplorerIdlePool
	for rows.Next() {
		var p ExplorerIdlePool
		if err := rows.Scan(&p.Venue, &p.VenueType, &p.Balance, &p.Equity,
			&p.FreeMargin, &p.Currency, &p.FetchedAt, &p.BufferMultiple); err != nil {
			return nil, fmt.Errorf("scan idle snapshot: %w", err)
		}
		pools = append(pools, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 近30d收入（venue_key 归一键优先，裸 venue 兼容——D-496 双键惯例）。
	income := map[string]float64{}
	irows, err := s.pool.Query(ctx, `
		SELECT venue, COALESCE(SUM(amount_usd),0) FROM income_events
		WHERE ts >= $1 GROUP BY venue`, now.Add(-30*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("explorer idle income: %w", err)
	}
	for irows.Next() {
		var v string
		var sum float64
		if err := irows.Scan(&v, &sum); err != nil {
			irows.Close()
			return nil, fmt.Errorf("scan idle income: %w", err)
		}
		income[v] = sum
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return nil, err
	}
	for i := range pools {
		p := &pools[i]
		p.Income30dUSD = income[p.Venue]
		p.BelowFloor = p.BufferMultiple > 0 && p.BufferMultiple < 1
		p.Idle = p.Balance > 0 && p.Income30dUSD == 0
	}
	return pools, nil
}

// ExplorerStateGet explorer_state 读（缺键 → ok=false 非错误）。
func (s *Store) ExplorerStateGet(ctx context.Context, key string) (json.RawMessage, bool, error) {
	var val json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT val FROM explorer_state WHERE key=$1`, key).Scan(&val)
	if err != nil {
		return nil, false, nil
	}
	return val, true, nil
}

// ExplorerStatePut explorer_state 幂等写（同键覆盖——A4 快照幂等锚）。
func (s *Store) ExplorerStatePut(ctx context.Context, key string, val json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO explorer_state (key, val) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET val = EXCLUDED.val, updated_at = now()`, key, val)
	if err != nil {
		return fmt.Errorf("explorer state put: %w", err)
	}
	return nil
}

// ExplorerProposal 提案落库行（explorer_proposals；scan_date 由调用方按 BJ 历日算好）。
type ExplorerProposal struct {
	ScanDate  time.Time
	ScanKind  string
	Theme     string
	Canonical string
	BestPair  string
	Direction string
	Rationale string
	Evidence  json.RawMessage
}

// ExplorerProposalInsert 批量落库；dedupe 唯一索引冲突跳过（同日重跑幂等）。
// 返回实际插入行数（skipped=n-inserted 由调用方计 drop/重复）。
func (s *Store) ExplorerProposalInsert(ctx context.Context, ps []ExplorerProposal) (int, error) {
	n := 0
	for _, p := range ps {
		ev := p.Evidence
		if len(ev) == 0 {
			ev = json.RawMessage(`{}`)
		}
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO explorer_proposals
				(scan_date, scan_kind, theme, canonical, best_pair, direction, rationale, evidence)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (scan_date, scan_kind, canonical, best_pair) DO NOTHING`,
			p.ScanDate, p.ScanKind, p.Theme, p.Canonical, p.BestPair, p.Direction, p.Rationale, ev)
		if err != nil {
			return n, fmt.Errorf("explorer proposal insert: %w", err)
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
