// D-208 NULL-case 回归测试（D-209 S2 欠账）：
// personal_positions 插入 cmc_instrument_id=NULL 行（staking 型）+ 非 NULL 行（short_hedge），
// 走 ListPersonalPositions 扫描路径断言不炸、两行返回（2026-09-03 生产 500 的正反弹）。
package pgstore

import (
	"context"
	"testing"
)

// TestListPersonalPositions_NullCMCInstrumentID D-208 止血正反弹：
// cmc_instrument_id=NULL 行（staking）+ 非 NULL 行（short_hedge）共存，
// ListPersonalPositions 扫描不炸（非指针 string 扫 NULL 即 500，止血改 *string）。
func TestListPersonalPositions_NullCMCInstrumentID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 确保表+列存在（migrate 已跑，但显式幂等）
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS personal_positions (
			id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			asset        TEXT NOT NULL,
			kind         TEXT NOT NULL CHECK (kind IN ('staking','short_hedge','other')),
			price_symbol TEXT NOT NULL,
			warn_high    DOUBLE PRECISION,
			crit_high    DOUBLE PRECISION,
			note         TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','CLOSED')),
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			closed_at    TIMESTAMPTZ
		)
	`); err != nil {
		t.Fatalf("create personal_positions: %v", err)
	}
	for _, col := range []string{
		"ADD COLUMN IF NOT EXISTS qty DOUBLE PRECISION",
		"ADD COLUMN IF NOT EXISTS entry_price DOUBLE PRECISION",
		"ADD COLUMN IF NOT EXISTS leverage INTEGER",
		"ADD COLUMN IF NOT EXISTS opened_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS cmc_instrument_id VARCHAR(32)",
	} {
		if _, err := s.pool.Exec(ctx, "ALTER TABLE personal_positions "+col); err != nil {
			t.Fatalf("alter personal_positions %s: %v", col, err)
		}
	}
	if _, err := s.pool.Exec(ctx, "TRUNCATE personal_positions CASCADE"); err != nil {
		t.Fatalf("truncate personal_positions: %v", err)
	}

	// 插入 staking 行（cmc_instrument_id=NULL）+ short_hedge 行（cmc_instrument_id='X-AFQUL'）
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO personal_positions (asset, kind, price_symbol, warn_high, crit_high, note, cmc_instrument_id)
		VALUES ('TRX', 'staking', 'TRXUSDT', NULL, NULL, 'staking row', NULL),
		       ('TRX', 'short_hedge', 'TRXUSDT', 0.36, 0.3695, 'hedge row', 'X-AFQUL')
	`); err != nil {
		t.Fatalf("insert personal_positions: %v", err)
	}

	// 扫描路径——非指针 string 扫 NULL 即 500（止血前的 bug）
	positions, err := s.ListPersonalPositions(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("ListPersonalPositions with NULL cmc_instrument_id: %v", err)
	}
	if len(positions) != 2 {
		t.Fatalf("got %d positions, want 2", len(positions))
	}

	// 验证 NULL 行的 CMCInstrumentID==nil，非 NULL 行的 CMCInstrumentID 指向值
	var stakingRow, hedgeRow *PersonalPosition
	for i := range positions {
		switch positions[i].Kind {
		case "staking":
			stakingRow = &positions[i]
		case "short_hedge":
			hedgeRow = &positions[i]
		}
	}
	if stakingRow == nil || hedgeRow == nil {
		t.Fatalf("missing rows: staking=%v hedge=%v", stakingRow, hedgeRow)
	}
	if stakingRow.CMCInstrumentID != nil {
		t.Errorf("staking CMCInstrumentID = %v, want nil", *stakingRow.CMCInstrumentID)
	}
	if hedgeRow.CMCInstrumentID == nil || *hedgeRow.CMCInstrumentID != "X-AFQUL" {
		t.Errorf("hedge CMCInstrumentID = %v, want 'X-AFQUL'", hedgeRow.CMCInstrumentID)
	}
}
