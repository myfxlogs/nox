// income_events 存储（D-334ab 位点①）：binance FUNDING_FEE 实数轨。
// estimated 轨（position_cashflows，swapledger 每腿每日单行）不动；本表=venue 侧实结算流水，
// tranId 三列唯一幂等 + OPEN 腿匹配位可空（唯一命中才填，零/多义留 NULL 不猜）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// IncomeEvent income_events 行（PositionID/LegNo=匹配命中才非 nil）。
// F1：账户身份=exchange_config_id（FK→exchange_configs.id，同 venue 多账户区分）；
// (Venue,VenueType) 为 R1/R2 通道型标注列。
type IncomeEvent struct {
	Venue      string
	VenueType  string
	ConfigID   int64 // exchange_configs.id（0=历史/未知账户行）
	Symbol     string
	IncomeType string
	AmountUSD  float64
	TranID     int64
	Ts         time.Time
	PositionID *int64
	LegNo      *int32
}

// MaxIncomeEventTS 采集游标：该 (venue,exchange_config_id) 账户已入账最大 ts
// （表内派生零新状态，重启安全；F1 恢复 config_id 粒度——同 venue 多账户域下游标不共享）。
// 空表 → (零值,false,nil)。
func (s *Store) MaxIncomeEventTS(ctx context.Context, venue string, configID int64) (time.Time, bool, error) {
	var ts *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT MAX(ts) FROM income_events WHERE venue = $1 AND exchange_config_id = $2`, venue, configID).Scan(&ts)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("max income ts: %w", err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

// InsertIncomeEvents 批量幂等写入（唯一键 (exchange_config_id,tran_id) 冲突 DO NOTHING）。
// 返回实插行数（幂等重跑=0）。
func (s *Store) InsertIncomeEvents(ctx context.Context, evs []IncomeEvent) (int64, error) {
	if len(evs) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, e := range evs {
		batch.Queue(`
			INSERT INTO income_events (venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts, position_id, leg_no)
			VALUES ($1, $2, NULLIF($3,0), $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (exchange_config_id, tran_id) DO NOTHING`,
			e.Venue, e.VenueType, e.ConfigID, e.Symbol, e.IncomeType, e.AmountUSD, e.TranID, e.Ts, e.PositionID, e.LegNo)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	var written int64
	for range evs {
		cmd, err := br.Exec()
		if err != nil {
			return written, fmt.Errorf("insert income event: %w", err)
		}
		written += cmd.RowsAffected()
	}
	return written, nil
}

// SumIncomeForDay D-349 S1：CEX perp 腿对账实际值——venue+symbol 当日 income 实收日和。
// 账户级口径（不绑 position_id——income 行 position_id 匹配为账本增强非对账前提，D-334ab 可空）。
// 返回 (金额和, 行数)；行数供「0 行=未到结算窗」语义（无行≠实收 0）。
func (s *Store) SumIncomeForDay(ctx context.Context, venue, symbol string, day time.Time) (float64, int, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var sum float64
	var rows int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0), COUNT(*) FROM income_events
		 WHERE (venue = $1 OR venue_key = $1) AND symbol = $2 AND ts >= $3 AND ts < $4`,
		venue, symbol, start, end).Scan(&sum, &rows)
	if err != nil {
		return 0, 0, fmt.Errorf("sum income for day: %w", err)
	}
	return sum, rows, nil
}

// SumIncomeWindow D-474c S1：demo 行 accrue 实收源——(venue,symbol) 窗口内
// income_events.amount_usd 累加。归属粒度上限=venue+symbol（同所同币多仓会并账，
// 调用方须 flag_warn 注记）；缺席窗返回 (0,0) 非错误（+0 不外推）；
// 查询/扫描错误上抛（cashflow-write-error-silently-dropped KB：禁吞）。
func (s *Store) SumIncomeWindow(ctx context.Context, venue, symbol string, from, to time.Time) (float64, int, error) {
	var sum float64
	var rows int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0), COUNT(*) FROM income_events
		 WHERE (venue = $1 OR venue_key = $1) AND symbol = $2 AND ts >= $3 AND ts < $4`,
		venue, symbol, from, to).Scan(&sum, &rows)
	if err != nil {
		return 0, 0, fmt.Errorf("sum income window %s %s: %w", venue, symbol, err)
	}
	return sum, rows, nil
}

// MatchOpenLegForIncome income 行→OPEN 仓腿匹配：venue+symbol 在活跃仓（四态，同
// positions_active_dedup_key_idx 口径）OPEN 腿中**唯一命中**才返回 (posID, legNo, 1)；
// 零命中 → (0,0,0)；多义 → (last, hits>1)——调用方按 hits==1 才采信（不猜，venue 侧可重查）。
// env=demo/live 环境一致性——sim 配置事件只配 environment='demo' 腿、
// real 只配 live（NULL 归 live）；venue 传全形（"binance:perp"=position_legs.venue 口径）。
func (s *Store) MatchOpenLegForIncome(ctx context.Context, venue, symbol, env string) (posID int64, legNo int16, hits int, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, l.leg_no
		FROM position_legs l
		JOIN positions p ON p.id = l.position_id
		WHERE l.venue_key = $1 AND l.symbol = $2 AND l.phase = 'OPEN'
		  AND p.status IN ('OPENING','OPEN','LEGGED','CLOSING')
		  AND COALESCE(p.environment, 'live') = $3`, venue, symbol, env)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("match open leg: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&posID, &legNo); err != nil {
			return 0, 0, 0, err
		}
		hits++
	}
	return posID, legNo, hits, rows.Err()
}

// OpenLegSymbols D-334ab S3：指定 venue 活跃仓（四态）OPEN 腿的 distinct symbol 集
// （income 结算对齐定时器的符号集=白名单∪本查询）。
func (s *Store) OpenLegSymbols(ctx context.Context, venue string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT l.symbol
		FROM position_legs l
		JOIN positions p ON p.id = l.position_id
		WHERE l.venue_key = $1 AND l.phase = 'OPEN'
		  AND p.status IN ('OPENING','OPEN','LEGGED','CLOSING')`, venue)
	if err != nil {
		return nil, fmt.Errorf("open leg symbols: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sym string
		if err := rows.Scan(&sym); err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}

// SumIncomeByPosVenue D-375 S1：批量返回 (position_id, venue) → income 实收合计
// （「息差已收」列实收源——展示侧不重做匹配，P3：匹配逻辑单点在 collector 插入期）。
// key 格式 = posID\x00venue（venue=position_legs.venue 全形如 "binance:perp"）。
// join 必带 l.phase='OPEN'——同 leg_no 有 OPEN+CLOSE 两行（pos494 实证），漏则 SUM 翻倍；
// position_id NULL 行（未匹配，不猜）经 join+ANY 天然排除。
func (s *Store) SumIncomeByPosVenue(ctx context.Context, posIDs []int64) (map[string]float64, error) {
	out := make(map[string]float64)
	if len(posIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ie.position_id, l.venue_key, COALESCE(SUM(ie.amount_usd), 0)
		FROM income_events ie
		JOIN position_legs l ON l.position_id = ie.position_id AND l.leg_no = ie.leg_no AND l.phase = 'OPEN'
		WHERE ie.position_id = ANY($1)
		GROUP BY ie.position_id, l.venue_key`,
		posIDs)
	if err != nil {
		return nil, fmt.Errorf("sum income by pos venue: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var posID int64
		var venue string
		var sum float64
		if err := rows.Scan(&posID, &venue, &sum); err != nil {
			return nil, fmt.Errorf("scan income sum: %w", err)
		}
		out[fmt.Sprintf("%d\x00%s", posID, venue)] = sum
	}
	return out, rows.Err()
}
