// d592_dedup_test.go D-592 S2：直推旁路同 key 对齐——飞书推送判定与 insertAlert
// 漏斗共用归一键（原前缀式对异型 fatal 过抑制已退役）。
package main

import (
	"context"
	"testing"
)

func TestD592_FatalSameKeyAlign(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts`); err != nil {
		t.Fatal(err)
	}
	msg1 := "xasset-monitor fetch binance PAXGUSDT: premiumIndex 418"
	if err := s.InsertAlert(ctx, nil, "critical", msg1); err != nil {
		t.Fatal(err)
	}
	if !hasRecentSameKeyAlert(ctx, s, "critical", msg1) {
		t.Fatal("同键 fatal 在档应判定真（推判定抑制）")
	}
	if hasRecentSameKeyAlert(ctx, s, "critical", "xasset-monitor fetch binance XAUTUSDT: premiumIndex 418") {
		t.Fatal("异键 fatal 不应互抑（原前缀式过抑制已退役）")
	}
}
