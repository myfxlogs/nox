// d515_seed_test.go —— D-515 S4：seedConfigFromEnv 空表播种+幂等锚（真实 _test 隔离库）。
// A5 覆盖：播种一次入库 / 二次 boot 表非空不覆盖（env 改动不回写）/ secrets 走加密列。
package main

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"

	"github.com/jackc/pgx/v5"
)

func TestD515SeedConfigFromEnv(t *testing.T) {
	ctx := context.Background()
	st, err := pgstore.New(ctx, testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore new: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	raw, err := pgx.Connect(ctx, testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close(ctx)
	for _, q := range []string{`DELETE FROM app_config`, `DELETE FROM app_secrets`} {
		if _, err := raw.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("NOX_OPP_NOTIONAL_USD", "4321")
	t.Setenv("NOX_MASTER_KEY", "testkey-d515")
	t.Setenv("NOX_FEISHU_APP_SECRET", "fs-secret-1")

	seedConfigFromEnv(ctx, st)

	rows, err := st.LoadAppConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows["NOX_OPP_NOTIONAL_USD"] != "4321" {
		t.Fatalf("seeded value: %v", rows["NOX_OPP_NOTIONAL_USD"])
	}
	// secrets 走 app_secrets 加密列（app_config 零残留明文）。
	if _, ok := rows["NOX_FEISHU_APP_SECRET"]; ok {
		t.Fatal("secret must not be seeded into app_config")
	}
	secrets, err := st.LoadAppSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if secrets["NOX_FEISHU_APP_SECRET"] != "fs-secret-1" {
		t.Fatal("secret seed decrypt mismatch")
	}

	// 二次 boot：env 改值 + 手工改库行 → seed 不覆盖（非空纯读）。
	t.Setenv("NOX_OPP_NOTIONAL_USD", "9999")
	t.Setenv("NOX_FEISHU_APP_SECRET", "fs-secret-2")
	if _, err := raw.Exec(ctx, `UPDATE app_config SET value='777' WHERE key='NOX_OPP_NOTIONAL_USD'`); err != nil {
		t.Fatal(err)
	}
	seedConfigFromEnv(ctx, st)
	rows, _ = st.LoadAppConfig(ctx)
	if rows["NOX_OPP_NOTIONAL_USD"] != "777" {
		t.Fatalf("second-boot seed must not overwrite: %v", rows["NOX_OPP_NOTIONAL_USD"])
	}
	secrets, _ = st.LoadAppSecrets(ctx)
	if secrets["NOX_FEISHU_APP_SECRET"] != "fs-secret-1" {
		t.Fatal("second-boot seed must not overwrite secrets")
	}
}
