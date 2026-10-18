package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestFatal_DispatchLine D-622 三批：fetch critical 末行实施席转发令。
func TestFatal_DispatchLine(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE 'xasset-monitor%'`); err != nil {
		t.Fatal(err)
	}
	reportFatal(ctx, s, errors.New("fetch binance XAUTUSDT: premiumIndex 418"))
	var msg string
	if err := s.Pool().QueryRow(ctx,
		`SELECT message FROM alerts WHERE level='critical' ORDER BY id DESC LIMIT 1`).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(msg, "[角色:实施] xasset 采集排障（读 STATE 本行）") {
		t.Fatalf("末行令缺: %q", msg)
	}
}
