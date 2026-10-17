// strategy_retire_test.go —— D-614 新生宽限锚。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestStrategyRetireVerdict_NewbornGrace D-614：模板注册 <idle 窗 → 零活动不退（出生即死病灶）；
// 模板过窗 → 零活动照退。PnL 硬线优先于宽限（灾难即杀）。
func TestStrategyRetireVerdict_NewbornGrace(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	const key = "D614NB"
	// 新生模板（updated_at=now）
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_opp_template (id, mechanism, net_apr, payoff_class, updated_at) VALUES ($1,'test',0,'yield_on_holdings',now())`, key); err != nil {
		t.Fatal(err)
	}
	if v, err := st.StrategyRetireVerdict(ctx, key, 14*24*time.Hour, -50); err != nil || v != "" {
		t.Fatalf("新生零活动应不退：v=%q err=%v", v, err)
	}
	// 模板过窗 → 照退
	if _, err := st.Pool().Exec(ctx,
		`UPDATE fin_opp_template SET updated_at=now()-interval '20 days' WHERE id=$1`, key); err != nil {
		t.Fatal(err)
	}
	if v, err := st.StrategyRetireVerdict(ctx, key, 14*24*time.Hour, -50); err != nil || v == "" {
		t.Fatalf("过窗零活动应退：v=%q err=%v", v, err)
	}
}
