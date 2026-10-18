// 权重自限闸锚测（418 自限票——X-MBX-USED-WEIGHT 头族实读预节流件）
package main

import (
	"net/http"
	"testing"
)

func TestUsedWeight_HeaderFamily(t *testing.T) {
	h := http.Header{}
	h.Set("X-MBX-USED-WEIGHT", "100")
	h.Set("X-MBX-USED-WEIGHT-1M", "800")
	if got := usedWeight(h); got != "800" {
		t.Fatalf("1M 族优先取大者: got %q want 800", got)
	}
	h2 := http.Header{}
	h2.Set("X-MBX-USED-WEIGHT", "200")
	if got := usedWeight(h2); got != "200" {
		t.Fatalf("legacy 兜底: got %q want 200", got)
	}
	if got := usedWeight(http.Header{}); got != "" {
		t.Fatalf("零头=空: got %q", got)
	}
}

func TestWeightGate_AbortAndPass(t *testing.T) {
	// cap=1200 pct=80 → 阈=960：981>960 截，900 过，头缺=闸无据不动
	if !weightGate(981, 1200, 80) {
		t.Fatal("超阈未截")
	}
	if weightGate(900, 1200, 80) {
		t.Fatal("余量内误截")
	}
	if weightGate(0, 1200, 80) || weightGate(999, 0, 80) {
		t.Fatal("闸无据误动（零头/零 cap）")
	}
}
