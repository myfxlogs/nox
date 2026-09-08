package pgstore

import (
	"context"
	"encoding/json"
	"testing"

	"arb/internal/testutil"
)

// D-259 S1: signal_config store helper — defaults + config-driven override + missing-key log.
func TestLoadCNFIFundSignalConfig_DefaultsAndOverride(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_config WHERE key IN ('underperf_bps','layer_dd_pct','rank_shift','alerts_enabled')`)
	})

	// 1) 无配置 → 保守默认
	cfg := CNFIFundSignalConfigDefaults()
	got, err := st.LoadCNFIFundSignalConfig(ctx)
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if got != cfg {
		t.Fatalf("defaults mismatch: got %+v want %+v", got, cfg)
	}

	// 2) 写入 4 键 → 读取覆盖
	vals := map[string]any{
		"underperf_bps":  15.0,
		"layer_dd_pct":   0.3,
		"rank_shift":     3,
		"alerts_enabled": true,
	}
	for k, v := range vals {
		raw, _ := json.Marshal(v)
		if err := st.UpsertCNFISignalConfigRaw(ctx, k, raw, "D-259-test", "test"); err != nil {
			t.Fatalf("upsert config %s: %v", k, err)
		}
	}
	got, err = st.LoadCNFIFundSignalConfig(ctx)
	if err != nil {
		t.Fatalf("load override: %v", err)
	}
	if got.UnderperfBps != 15.0 || got.LayerDDPct != 0.3 || got.RankShift != 3 || !got.AlertsEnabled {
		t.Fatalf("override mismatch: got %+v", got)
	}

	// 3) CNFISignalStateValue — 不存在 → nil
	v, err := st.CNFISignalStateValue(ctx, "nonexistent_key_d259")
	if err != nil || v != nil {
		t.Fatalf("nonexistent state value: v=%v err=%v", v, err)
	}

	// 4) CNFISignalStateValue — 存在 → 值
	if err := st.UpsertCNFISignalState(ctx, "d259_test_val", "observe", 42.5, 1, "2099-01-01", "test"); err != nil {
		t.Fatalf("upsert state: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_state WHERE key='d259_test_val'`) })
	v, err = st.CNFISignalStateValue(ctx, "d259_test_val")
	if err != nil || v == nil || *v != 42.5 {
		t.Fatalf("state value: v=%v err=%v", v, err)
	}

	// 5) CNFIActiveSignalKeys — 7 天内存在
	keys, err := st.CNFIActiveSignalKeys(ctx, 7)
	if err != nil {
		t.Fatalf("active keys: %v", err)
	}
	if !keys["d259_test_val"] {
		t.Fatalf("active keys missing d259_test_val: %v", keys)
	}
}
