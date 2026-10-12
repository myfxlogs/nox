// d512_t8_env_gate_test.go —— D-512 T8：聚合闸红测（legacy_demo/NULL 不进 live 聚合）。
// 先红锚：0148 前旧行为=COALESCE(environment,'live') 把 NULL 静默当 live 计入（476 行污染面）。
package pgstore

import (
	"context"
	"strconv"
	"testing"
)

func TestD512_T8_LegacyDemoNotInLiveAggregates(t *testing.T) {
	st := sharedTestStore(t) // 全量 Migrate 同态 schema（含 0148/0149）
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE positions, position_settlements RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Pool().Exec(ctx, `TRUNCATE positions, position_settlements RESTART IDENTITY CASCADE`)
	})

	seed := func(env any, status, typ string, notional, realized float64) {
		var envExpr string
		if env == nil {
			envExpr = "NULL"
		} else {
			envExpr = "'" + env.(string) + "'"
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO positions
			(opportunity_id, type, status, expected_net_bps, environment, notional_usd, realized_pnl, opened_at, closed_at)
			VALUES ('d512-t8','`+typ+`','`+status+`',10,`+envExpr+`,`+
			ftoaD512(notional)+`,`+ftoaD512(realized)+`, now()-interval '2h', now()-interval '1h')`); err != nil {
			t.Fatal(err)
		}
	}
	// live 行 100 + legacy（NULL 环境）行 700 + unknown 行 300。
	seed("live", "OPEN", "XASSET_CARRY", 100, 0)
	seed(nil, "OPEN", "XASSET_CARRY", 700, 0)
	seed("unknown", "OPEN", "XASSET_CARRY", 300, 0)
	// 已平仓聚合行：live +5 / legacy NULL 环境 +50。
	seed("live", "CLOSED", "XASSET_CARRY", 0, 5)
	seed(nil, "CLOSED", "XASSET_CARRY", 0, 50)

	// 锚 1：ActiveNotionalByEnv——legacy_demo/unknown 不得进 live 行；live=100。
	envRows, err := st.ActiveNotionalByEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range envRows {
		if r.Env == "live" && r.NotionalUSD != 100 {
			t.Fatalf("live 行应=100（NULL/unknown 不计入），got %v rows=%+v", r.NotionalUSD, envRows)
		}
		if r.Env != "live" {
			t.Errorf("聚合面板只出 live 行，出现 %q（legacy 应彻底不进面板）", r.Env)
		}
	}
	// 锚 2：AggregateStrategyStatsByEnvironment("XASSET_CARRY","live")——不含 legacy 50。
	stat, err := st.AggregateStrategyStatsByEnvironment(ctx, "XASSET_CARRY", "live")
	if err != nil {
		t.Fatal(err)
	}
	if stat.AvgPnl > 5.001 {
		t.Fatalf("live 聚合混入 legacy/unknown 行：avg=%v（want ≤5=仅 live 行）", stat.AvgPnl)
	}
	// 锚 3：AggregateStrategyStatsByEnvironment("XASSET_CARRY","")全量口径也不含 NULL 行。
	statAll, err := st.AggregateStrategyStatsByEnvironment(ctx, "XASSET_CARRY", "")
	if err != nil {
		t.Fatal(err)
	}
	if statAll.AvgPnl > 5.001 {
		t.Fatalf("全量口径 NULL 行泄漏：avg=%v（want ≤5）", statAll.AvgPnl)
	}
	// 锚 4：DailyRealizedPnlByEnvironment("live")——不含 legacy 50。
	sum, err := st.DailyRealizedPnlByEnvironment(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	if sum > 5.001 {
		t.Fatalf("daily live sum=%v 混入 legacy 50", sum)
	}
}

func ftoaD512(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
