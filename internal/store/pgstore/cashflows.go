package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nox/internal/data/keydomain"

	"github.com/jackc/pgx/v5"
)

// Cashflow position_cashflows 行。
type Cashflow struct {
	ID         int64
	PositionID int64
	LegNo      *int16 // nil=legacy（D-193-A）
	Kind       string
	Venue      string
	AmountUSD  float64
	Ts         time.Time
	// 事实身份字段（设计 §3.5）
	AccountID       *int64
	RawCurrency     *string // D-193 审计 P1-2：字段名对齐设计 §3.5
	RawAmount       *float64
	FXRate          *float64
	FXSource        *string
	EventKey        *string
	ExternalEventID *string // D-193 审计 P1-1：外部原始事件 ID
	Source          *string
	Quality         *string // actual/estimated/unknown；nil=legacy
	SourceTs        *time.Time
	ReceivedTs      *time.Time
	OccurredAt      *time.Time // D-193 审计 P1-1：现金流发生时间
}

// InsertCashflow 写入现金流。
// 加 legNo 参数（per-leg 幂等键前置）。
// legNo 是 position_legs.leg_no（int16，从 0 开始，0 是有效第一腿，写 0 不是 NULL）。
func (s *Store) InsertCashflow(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error {
	vk := keydomain.Parse(venue) // D-496：venue 传 venueKey 复合键，落库拆两列
	q := `INSERT INTO position_cashflows (position_id, leg_no, kind, venue, venue_type, amount_usd, ts)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	_, err := s.pool.Exec(ctx, q, posID, legNo, kind, vk.Venue, vk.Type, amount, ts)
	return err
}

// ListCashflows 返回指定持仓的现金流（§13.7 复盘合算：realized_pnl + Σcashflows）。
// 扫描新事实身份字段（指针容错 legacy 行 NULL）。
func (s *Store) ListCashflows(ctx context.Context, posID int64) ([]Cashflow, error) {
	q := `SELECT id, position_id, leg_no, kind, venue_key AS venue, amount_usd, ts,
			account_id, raw_currency, raw_amount, fx_rate, fx_source, event_key,
			external_event_id, source, quality, source_ts, received_ts, occurred_at
		FROM position_cashflows WHERE position_id = $1 ORDER BY ts`
	rows, err := s.pool.Query(ctx, q, posID)
	if err != nil {
		return nil, fmt.Errorf("list cashflows: %w", err)
	}
	defer rows.Close()
	var out []Cashflow
	for rows.Next() {
		var c Cashflow
		if err := rows.Scan(&c.ID, &c.PositionID, &c.LegNo, &c.Kind, &c.Venue, &c.AmountUSD, &c.Ts,
			&c.AccountID, &c.RawCurrency, &c.RawAmount, &c.FXRate, &c.FXSource, &c.EventKey,
			&c.ExternalEventID, &c.Source, &c.Quality, &c.SourceTs, &c.ReceivedTs, &c.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HasCashflowForLegDay 检查指定持仓某腿在某日是否已有某 kind 的现金流行（D-159 W3a R5 per-leg 幂等）。
// day 为 UTC 日期（0 时），匹配该日 00:00~23:59:999 的行；替代 position 级 HasCashflowForDay
// （position 级短路会让失败腿永久漏记，R5：一腿失败下一轮只补失败腿）。
func (s *Store) HasCashflowForLegDay(ctx context.Context, posID int64, legNo int16, kind string, day time.Time) (bool, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM position_cashflows
		 WHERE position_id = $1 AND leg_no = $2 AND kind = $3 AND ts >= $4 AND ts < $5 LIMIT 1`,
		posID, legNo, kind, start, end).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check cashflow for leg day: %w", err)
}

// HasCashflowByEventKey D-422 R1：按 event_key 精确匹配判重（幂等键粒度，
// 替代 HasCashflowForLegDay 的腿日粗粒度——同日 OPEN 费与 CLOSE 佣金不同 event_key 不互吞）。
func (s *Store) HasCashflowByEventKey(ctx context.Context, posID int64, eventKey string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM position_cashflows WHERE position_id = $1 AND event_key = $2 LIMIT 1`,
		posID, eventKey).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check cashflow by event_key: %w", err)
}

// SumCashflowsNet 返回指定持仓 swap+funding+commission 现金流的 USD 合计（D-127 P4 返修①）。
// settlement.Settle 用此值算 net = realized + cashflowsNet，修正淘汰/复盘口径漏计息差/手续费的缺陷。
// commission 写入时已带负号（pipeline_legs.go:190 -fill.Commission），SUM 自动减 → 等价 swap+funding−commission。
// 只统计 quality='actual' 或 quality IS NULL（legacy 兼容）的现金流；
// quality='estimated'/'unknown' 的现金流不进入正式实际收益统计。
// 无匹配行 → 返回 0（不影响 net）。
func (s *Store) SumCashflowsNet(ctx context.Context, posID int64) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0) FROM position_cashflows
		 WHERE position_id = $1 AND kind IN ('swap','funding','commission')
		   AND (quality = 'actual' OR quality IS NULL)`,
		posID).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum cashflows net: %w", err)
	}
	return sum, nil
}

// SumPositionCarryActual D-037：指定持仓已收息差合计（position_cashflows
// kind IN ('funding','swap')，quality='actual'∨NULL legacy 同 SumCashflowsNet 口径；
// commission 不计——平仓净估的费项已含退出费，息差=纯已收 funding/swap）。
// 无匹配行 → 0。
func (s *Store) SumPositionCarryActual(ctx context.Context, posID int64) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd), 0) FROM position_cashflows
		 WHERE position_id = $1 AND kind IN ('funding','swap')
		   AND (quality = 'actual' OR quality IS NULL)`,
		posID).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum position carry actual: %w", err)
	}
	return sum, nil
}

// InsertCashflowFull D-193-A：完整字段写入（事实幂等键 event_key + 调用方按证据等级传 quality）。
// 扩包（决策 3）：quality 由调用方按证据等级传入——公式路径传 'estimated'，
// 对拍金额匹配后升 'actual'，不匹配置 'unknown'。本函数不强制 quality=actual。
// event_key 为事实幂等键；ON CONFLICT(event_key) DO NOTHING。
// 返回 won=true 表示新插入，won=false 表示已存在（幂等成功）。
//
// 审计 P1-14 正负号约定（设计 §3.5 硬约束）：
// amount_usd 正负号按实际现金流方向保存——swap/funding 按实收（正=收入），
// commission 按实付（负=支出，与现有 pipeline_legs.go:190 -fill.Commission 口径一致）。
// 调用方传入的 amount_usd 必须已带正确正负号，本函数不做符号翻转。
func (s *Store) InsertCashflowFull(ctx context.Context, c *Cashflow) (won bool, err error) {
	vk := keydomain.Parse(c.Venue) // D-496：复合键拆两列
	var id int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO position_cashflows
			(position_id, leg_no, kind, venue, venue_type, amount_usd, ts,
			 account_id, raw_currency, raw_amount, fx_rate, fx_source, event_key,
			 external_event_id, source, quality, source_ts, received_ts, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (event_key) WHERE event_key IS NOT NULL DO NOTHING
		RETURNING id`,
		c.PositionID, c.LegNo, c.Kind, vk.Venue, vk.Type, c.AmountUSD, c.Ts,
		c.AccountID, c.RawCurrency, c.RawAmount, c.FXRate, c.FXSource, c.EventKey,
		c.ExternalEventID, c.Source, c.Quality, c.SourceTs, c.ReceivedTs, c.OccurredAt).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // ON CONFLICT 命中，幂等成功
		}
		return false, fmt.Errorf("insert cashflow full pos=%d: %w", c.PositionID, err)
	}
	return true, nil
}

// SumCashflowsNetWithQuality D-193-A：返回 (sum, quality)。
// quality = 'actual' 全部 actual → 'actual'
// quality 含 unknown → 'unknown'
// quality 含 estimated（无 unknown）→ 'estimated'
// 无匹配行 → (0, 'no_cashflows')
// settlement.Settle 用此判断 completeness。
func (s *Store) SumCashflowsNetWithQuality(ctx context.Context, posID int64) (sum float64, quality string, err error) {
	// 逐行读 quality 决定聚合质量（actual/estimated/unknown），SUM 只统计 actual+legacy。
	// legacy 行 quality IS NULL → 视作 actual（兼容旧数据，D-193 §3.6 legacy 标记在 settlement 侧）。
	var hasUnknown, hasEstimated bool
	// ②：commission 无条件计入（同 SumCashflowsNetForSettle 口径——
	// 的 fees_usd 双扣排除由 PositionNetUSD 单源接管，不再需要）。
	rows, err := s.pool.Query(ctx,
		`SELECT COALESCE(amount_usd, 0), COALESCE(quality, 'actual')
		 FROM position_cashflows
		 WHERE position_id = $1 AND kind IN ('swap','funding','commission')`,
		posID)
	if err != nil {
		return 0, "", fmt.Errorf("sum cashflows net with quality: %w", err)
	}
	defer rows.Close()
	total := 0.0
	count := 0
	for rows.Next() {
		var amt float64
		var q string
		if err := rows.Scan(&amt, &q); err != nil {
			return 0, "", err
		}
		count++
		switch q {
		case "unknown":
			hasUnknown = true
		case "estimated":
			hasEstimated = true
			total += amt // estimated 仍计入 sum（completeness 判定用），但 pnlQuality=estimated
		default: // actual 或 legacy（NULL→actual）
			total += amt
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	if count == 0 {
		return 0, "no_cashflows", nil
	}
	switch {
	case hasUnknown:
		return total, "unknown", nil
	case hasEstimated:
		return total, "estimated", nil
	default:
		return total, "actual", nil
	}
}

// SumEstimatedCashflowsByPos D-375 S1：批量返回 position_id → swap+funding estimated 公式计提合计
// （「息差已收」列的「估」参考行数据源——与实收（income_events/broker 快照）是两个世界）。
// actual/NULL quality 排除（实收不双计）；kind 过滤 commission 不计；无匹配行 → 该 key 不在 map。
func (s *Store) SumEstimatedCashflowsByPos(ctx context.Context, posIDs []int64) (map[int64]float64, error) {
	out := make(map[int64]float64)
	if len(posIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT position_id, COALESCE(SUM(amount_usd), 0)
		 FROM position_cashflows
		 WHERE position_id = ANY($1) AND kind IN ('swap','funding') AND quality = 'estimated'
		 GROUP BY position_id`,
		posIDs)
	if err != nil {
		return nil, fmt.Errorf("sum estimated cashflows by pos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var posID int64
		var sum float64
		if err := rows.Scan(&posID, &sum); err != nil {
			return nil, fmt.Errorf("scan estimated sum: %w", err)
		}
		out[posID] = sum
	}
	return out, rows.Err()
}
