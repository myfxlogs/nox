// equiv_guard_test.go —— 量级守卫锚：SPCXUSDT×SPX500 假等价拒产断言。
package main

import (
	"errors"
	"testing"
)

// |entry_spread|>equivGuardBps → runPair 返哨兵错（不产簿）；runPairs 消费
// =warn+stills 档非 fatal 升级（返修⑥钦定档对版）。
func TestEquivGuard_SPCXRejects(t *testing.T) {
	// 量级守卫闸件直验——SPCXUSDT(147.37)×SPX500(7668.1) spread≈−9808bps
	// 超 3000bps 阈。runPair 前置守卫须命中哨兵（模拟等价：直造哨兵判据）。
	spread := (147.37 - 7668.1) / 7668.1 * 10000
	if spread > -9000 || spread < -9900 {
		t.Fatalf("实证 spread 应 ≈-9808bps: %.0f", spread)
	}
	if err := equivGuardCheck("SPCXUSDT-SPX500", spread); !errors.Is(err, errEquivGuard) {
		t.Fatalf("超阈 spread 应产 errEquivGuard 哨兵: %v", err)
	}
	// 正常对（XAU 簿域）不撞闸：spread=-5bps 远低于阈。
	if err := equivGuardCheck("XAUUSDT-XAU", -5.0); err != nil {
		t.Fatalf("正常 spread 不应命中哨兵: %v", err)
	}
}
