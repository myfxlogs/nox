// finance_overview_test.go —— D-362-R2 S2 锚：FinanceFlows income 分支 #sim 标识。
// sim 配置（exchange_configs.simulated=t）来源行 source='binance#sim'；real 配置='binance'
// （venue 数据口径原值不动=核证点④，标识只进显示列）。R0 标识漏落实证：锚对未标识实现红。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestFinanceFlows_IncomeSimMarker(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		`TRUNCATE income_events, position_cashflows, ledger, exchange_configs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// exchange_configs：sim（后建恒赢槽位）+real 双配置（核证点③真值表，SSOT §0）。
	// id 为 IDENTITY 列 → RETURNING 捕获真 id 作 income_events.account_id 对号。
	var simID, realID int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		VALUES ('binance', 'perp', 'sim', 'k25', 's25', true) RETURNING id`).Scan(&simID); err != nil {
		t.Fatalf("seed sim config: %v", err)
	}
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, simulated)
		VALUES ('binance', 'perp', 'real', 'k20', 's20', false) RETURNING id`).Scan(&realID); err != nil {
		t.Fatalf("seed real config: %v", err)
	}
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO income_events (venue, account_id, symbol, income_type, amount_usd, tran_id, ts)
		VALUES ('binance', $1, 'XAUUSDT', 'FUNDING_FEE', -1.5, 1, $2),
		       ('binance', $3, 'XAUUSDT', 'FUNDING_FEE', -0.5, 2, $2)`, simID, now, realID); err != nil {
		t.Fatalf("seed income events: %v", err)
	}

	flows, err := s.FinanceFlows(ctx, 100)
	if err != nil {
		t.Fatalf("FinanceFlows: %v", err)
	}
	simMarked, realPlain := 0, 0
	for _, f := range flows {
		if f.Kind != "funding_fee" {
			continue
		}
		if f.AmountUSD == -1.5 {
			if f.Source == "binance#sim" {
				simMarked++
			} else {
				t.Errorf("sim 配置行 source = %q, want binance#sim", f.Source)
			}
		}
		if f.AmountUSD == -0.5 {
			if f.Source == "binance" {
				realPlain++
			} else {
				t.Errorf("real 配置行 source = %q, want binance（原值无标识）", f.Source)
			}
		}
	}
	if simMarked != 1 || realPlain != 1 {
		t.Fatalf("flows 命中 sim=%d real=%d, want 1/1（共 %d 行）", simMarked, realPlain, len(flows))
	}
}
