package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestClampWatermark 化石水位自愈锚（真 clampWatermark）：存量水位>库内实存
// max(id)→钳回上限；水位≤max 不动（向前重读增量不破）。
func TestClampWatermark(t *testing.T) {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, "postgres://nox:nox@127.0.0.1:5434/postgres")
	if err != nil {
		t.Skipf("test db admin unavailable: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx,
		`DROP DATABASE IF EXISTS digest_wm_test`); err != nil {
		t.Skipf("drop: %v", err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE digest_wm_test`); err != nil {
		t.Skipf("create: %v", err)
	}
	pool, err := pgxpool.New(ctx, "postgres://nox:nox@127.0.0.1:5434/digest_wm_test")
	if err != nil {
		t.Skipf("test db unavailable: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE autotrade_logs (id bigint, status text);
		CREATE TABLE alerts (id bigint, level text);
		INSERT INTO autotrade_logs VALUES (10,'X'),(20,'X'); INSERT INTO alerts VALUES (5,'c')`); err != nil {
		t.Fatal(err)
	}
	s := &digestState{AutotradeLogsMaxID: 89704, AlertsMaxID: 64904}
	if err := clampWatermark(ctx, pool, s); err != nil {
		t.Fatal(err)
	}
	if s.AutotradeLogsMaxID != 20 || s.AlertsMaxID != 5 {
		t.Fatalf("化石水位未钳回: %+v", s)
	}
	s2 := &digestState{AutotradeLogsMaxID: 3, AlertsMaxID: 5}
	if err := clampWatermark(ctx, pool, s2); err != nil {
		t.Fatal(err)
	}
	if s2.AutotradeLogsMaxID != 3 || s2.AlertsMaxID != 5 {
		t.Fatalf("真水位被误钳: %+v", s2)
	}
}
