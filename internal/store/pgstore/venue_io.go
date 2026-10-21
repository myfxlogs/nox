// venue_io.go —— D-365 S1：CEX 进出账流水存储（充值/提现，幂等落库+水位游标）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// VenueIO fin_venue_io 行（进出账一条：充值 deposit / 提现 withdraw）。
type VenueIO struct {
	Venue      string
	ConfigID   int64
	Direction  string // deposit / withdraw
	Ccy        string
	Amount     float64
	Fee        *float64
	Status     string // 交易所原生状态码原样存（枚举注释见采集器）
	TxID       string
	ExternalID string // binance.id·tranId / okx depId·wdId·billId
	Ts         time.Time
	// D-539①：内部划转（direction='transfer'）钱包两端——快照 venue_type 键域
	// （spot/funding/earn/perp/unified/margin）；充提行 NULL。
	FromWallet string
	ToWallet   string
}

// InsertVenueIO 批量幂等写入（唯一键 (venue,config_id,direction,external_id) 冲突 DO
// NOTHING）。返回实插行数（幂等重跑=0）。
func (s *Store) InsertVenueIO(ctx context.Context, rows []VenueIO) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO fin_venue_io (venue, config_id, direction, ccy, amount, fee, status, txid, external_id, ts, from_wallet, to_wallet)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (venue, config_id, direction, external_id) DO NOTHING`,
			r.Venue, r.ConfigID, r.Direction, r.Ccy, r.Amount, r.Fee, r.Status, r.TxID, r.ExternalID, r.Ts,
			r.FromWallet, r.ToWallet)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	var written int64
	for range rows {
		cmd, err := br.Exec()
		if err != nil {
			return written, fmt.Errorf("insert venue io: %w", err)
		}
		written += cmd.RowsAffected()
	}
	return written, nil
}

// LatestVenueIOTs 水位游标：该配置该方向已入账最大事件 ts（表内派生零新状态，重启安全）。
// 空表 → (零值,false,nil)。
func (s *Store) LatestVenueIOTs(ctx context.Context, configID int64, direction string) (time.Time, bool, error) {
	var ts *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT MAX(ts) FROM fin_venue_io WHERE config_id = $1 AND direction = $2`, configID, direction).Scan(&ts)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("latest venue io ts: %w", err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

// RecentVenueIO 财务页流水：该配置最近 N 行（deposit+withdraw 合并，ts 倒序）。
func (s *Store) RecentVenueIO(ctx context.Context, venue string, configID int64, limit int) ([]VenueIO, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT direction, ccy, amount, COALESCE(fee, 0), COALESCE(status, ''), COALESCE(txid, ''), ts
		FROM fin_venue_io
		WHERE venue = $1 AND config_id = $2
		ORDER BY ts DESC LIMIT $3`, venue, configID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent venue io: %w", err)
	}
	defer rows.Close()
	var out []VenueIO
	for rows.Next() {
		var r VenueIO
		if err := rows.Scan(&r.Direction, &r.Ccy, &r.Amount, &r.Fee, &r.Status, &r.TxID, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentIncomeByConfig 财务页收益流水：该 (venue,venue_type) 通道最近 N 条 income
// （funding fee/earn 利息等实收，D-365 S4 并入账户块）。
// SumVenueIOWalletOut (venue,ccy,from_wallet) 窗内划出额（earn→spot 赎回腿——
// D-630 earn_yield 伪影回补：赎回是划转非负收益，delta 须加回补）。
func (s *Store) SumVenueIOWalletOut(ctx context.Context, venue, ccy, wallet string, from, to time.Time) (float64, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount),0) FROM fin_venue_io
		WHERE venue=$1 AND ccy=$2 AND direction='transfer' AND from_wallet=$3 AND ts>$4 AND ts<=$5`,
		venue, ccy, wallet, from, to).Scan(&v)
	return v, err
}

func (s *Store) RecentIncomeByConfig(ctx context.Context, venue, venueType string, limit int) ([]IncomeEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT income_type, amount_usd, symbol, ts
		FROM income_events
		WHERE venue = $1 AND venue_type = $2 AND NOT is_transfer
		ORDER BY ts DESC LIMIT $3`, venue, venueType, limit)
	if err != nil {
		return nil, fmt.Errorf("recent income by config: %w", err)
	}
	defer rows.Close()
	var out []IncomeEvent
	for rows.Next() {
		var e IncomeEvent
		if err := rows.Scan(&e.IncomeType, &e.AmountUSD, &e.Symbol, &e.Ts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BalanceSnap D-602：fin_venue_account_snapshot 相邻差分窗（id/balance/ts 三元组）。
type BalanceSnap struct {
	ID      int64
	Balance float64
	Ts      time.Time
}

// BalanceDeltaWindows MT5 账户快照时序窗（balance_raw 相邻差分落账器输入——
// 升序返回；venue=account id 键）。
func (s *Store) BalanceDeltaWindows(ctx context.Context, venue string, since time.Time) ([]BalanceSnap, error) {
	// D-604：差分输入行限事件落地帧（mt_frame=流帧/mt_event=事件锚定写径）——
	// rest 系周期摄取快照（rest_poll/rest_calib/scheduler）禁入差分输入。
	// 小项⑶：id tiebreaker 防 fetched_at 同秒序不稳。
	rows, err := s.pool.Query(ctx, `
		SELECT id, balance_raw, fetched_at FROM fin_venue_account_snapshot
		WHERE venue=$1 AND fetched_at>=$2 AND balance_raw IS NOT NULL
		  AND balance_raw > 0 -- D-609：断连/nil-acct 零余额帧=连接伪影非状态变（43 行实拍）——零↔真交替产 ±全额垃圾残差
		  AND src IN ('mt_frame','mt_event')
		ORDER BY fetched_at, id`, venue, since)
	if err != nil {
		return nil, fmt.Errorf("balance snaps %s: %w", venue, err)
	}
	defer rows.Close()
	var out []BalanceSnap
	for rows.Next() {
		var b BalanceSnap
		if err := rows.Scan(&b.ID, &b.Balance, &b.Ts); err != nil {
			return nil, fmt.Errorf("scan snap: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ClosedPositionPnLBetween 窗口内关闭持仓的 realized_pnl 合计（差分残差抵扣——
// venue 腿命中的 positions.realized_pnl；PnL 入账≠外部充提）。
func (s *Store) ClosedPositionPnLBetween(ctx context.Context, venue string, from, to time.Time) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(p.realized_pnl),0) FROM positions p
		WHERE p.status='CLOSED' AND p.closed_at>$2 AND p.closed_at<=$3
		  AND EXISTS (SELECT 1 FROM position_legs l WHERE l.position_id=p.id AND l.venue=$1)`,
		venue, from, to).Scan(&sum)
	return sum, err
}

// JitNetOutstanding venue 内 JIT 归集净额：fin_venue_io 账 external_id
// jit-swp:（earn→交易户）正号 − jit-ret:（回转）负号差分——挪多少回多少的
// 权威锚（无独立状态表，账本自洽）。返回值<0 钳零（账异如实不虚报可回额）。
// JitArmedComboCount venue 腿上 armed（enabled 且非模拟）组合数——jit 回转
// 「下拍零 armed 机会」判定（D-647：零 armed=交易户闲置全回申 earn）。
func (s *Store) JitArmedComboCount(ctx context.Context, venue string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM xasset_allow_combos
		 WHERE enabled AND COALESCE(simulated,false)=false AND (venue_buy=$1 OR venue_sell=$1)`, venue).Scan(&n)
	return n, err
}

func (s *Store) JitNetOutstanding(ctx context.Context, venue string) (float64, error) {
	var net float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN external_id LIKE 'jit-swp:%' THEN amount
		                         WHEN external_id LIKE 'jit-ret:%' THEN -amount
		                         ELSE 0 END), 0)
		FROM fin_venue_io
		WHERE venue = $1 AND direction = 'transfer' AND external_id LIKE 'jit-%'`,
		venue).Scan(&net)
	if err != nil {
		return 0, fmt.Errorf("jit net outstanding %s: %w", venue, err)
	}
	if net < 0 {
		net = 0
	}
	return net, nil
}

// VenueIOExternalExists D-607：external_id 存在性查（Deal 事件↔ticket 扫描
// 双向查重联动——先到的留，防双记）。
func (s *Store) VenueIOExternalExists(ctx context.Context, configID int64, externalID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM fin_venue_io WHERE config_id=$1 AND external_id=$2)`,
		configID, externalID).Scan(&ok)
	return ok, err
}

// VenueIOTxIDExists D-607：txid 存在性查（ticket 扫描面反向查重——deal 先落
// 则 order 面 txid='order:{ticket}' 在库→跳过）。
func (s *Store) VenueIOTxIDExists(ctx context.Context, configID int64, txid string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM fin_venue_io WHERE config_id=$1 AND txid=$2)`,
		configID, txid).Scan(&ok)
	return ok, err
}

// ArmedDeliveryPair armed 交割配对行（D-649续③ 机会喂入环——xasset_allow_combos
// 含 futures 腿行+universe expiry 联查 tenor；symbol 按 futures 腿 canonical 对）。
type ArmedDeliveryPair struct {
	BuyCanonical, SellCanonical string
	BuyVenueKey, SellVenueKey   string
	BuyVT, SellVT               string
	GradSymbol                  string // carry_graduations.symbol（合约名锚；空=非毕业源）
	BuyExpiry, SellExpiry       *time.Time
}

// ListArmedDeliveryPairs armed（enabled 且非模拟）且任一端 vt=futures 的组合行——
// detector 交割配对枚举源；universe.expiry_ts 联查供 tenor 年化（无 expiry=tenor
// 未知 fail-closed）。
func (s *Store) ListArmedDeliveryPairs(ctx context.Context) ([]ArmedDeliveryPair, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.canonical_buy, c.canonical_sell, c.venue_key_buy, c.venue_key_sell,
		       c.venue_type_buy, c.venue_type_sell, COALESCE(g.symbol,''), ub.expiry_ts, us.expiry_ts
		  FROM xasset_allow_combos c
		  -- D-649续④：合约锚=provenance→carry_graduations.symbol（'ETH-USD_UM-261225'
		  -- 合约名）非 canonical（'ETH' 品种归一名）——universe 精确对毕业档键。
		  LEFT JOIN carry_graduations g ON g.id=c.provenance_id
		  LEFT JOIN fin_cex_universe ub
		    ON ub.venue=c.venue_buy AND ub.contract_type='FUTURES'
		   AND ub.symbol=CASE WHEN c.venue_type_buy='futures' THEN COALESCE(NULLIF(g.symbol,''),c.canonical_buy) ELSE c.canonical_buy END
		  LEFT JOIN fin_cex_universe us
		    ON us.venue=c.venue_sell AND us.contract_type='FUTURES'
		   AND us.symbol=CASE WHEN c.venue_type_sell='futures' THEN COALESCE(NULLIF(g.symbol,''),c.canonical_sell) ELSE c.canonical_sell END
		 WHERE c.enabled AND COALESCE(c.simulated,false)=false
		   AND (c.venue_type_buy='futures' OR c.venue_type_sell='futures')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArmedDeliveryPair
	for rows.Next() {
		var p ArmedDeliveryPair
		if err := rows.Scan(&p.BuyCanonical, &p.SellCanonical, &p.BuyVenueKey, &p.SellVenueKey,
			&p.BuyVT, &p.SellVT, &p.GradSymbol, &p.BuyExpiry, &p.SellExpiry); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
