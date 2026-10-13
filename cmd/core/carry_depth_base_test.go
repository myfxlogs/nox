// d057_depth_bases_test.go —— D-280 方案 A 锚：深度采集动态基名=各所 funding
// 事实见证符号经 carryDepthBase 归一（静态∪动态去重保序 / 读失败退化静态 /
// 后缀规范化 / 分所精集）。读口锚在 pgstore 侧（ListFundingWitnessedSymbols）。
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"nox/internal/data/collect/exchange"
)

// fakeDepthBaseLister fundingWitnessedDepthLister 测试替身（分所键：rows 按
// venue 分键，nil 键=全所同集）。
type fakeDepthBaseLister struct {
	rows map[string][]string
	err  error
}

func (f fakeDepthBaseLister) ListFundingWitnessedSymbols(_ context.Context, venue string, _ time.Duration) ([]string, error) {
	return f.rows[venue], f.err
}

func TestD057_CarryDepthBase(t *testing.T) {
	cases := map[string]string{
		"XAU":           "XAU",
		"XAUUSDT":       "XAU", // binance 形态 USDT 尾
		"XAU-USDT-SWAP": "XAU", // okx 永续形态
		"BTC-USDT-SWAP": "BTC", // 先剥 -USDT-SWAP 再剥 USDT
		"PAXGUSDT":      "PAXG",
		"XAU_USDT":      "XAU", // gate 形态：剥 USDT 后清悬空下划线
		"XAU-USDC-SWAP": "XAU-USDC-SWAP",
	}
	for in, want := range cases {
		if got := carryDepthBase(in); got != want {
			t.Errorf("carryDepthBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestD057_UniverseDepthBases(t *testing.T) {
	ctx := context.Background()

	// funding 见证 symbol → 基名规范化 + 去重保序
	fn := universeDepthBases(ctx, fakeDepthBaseLister{rows: map[string][]string{"binance": {"XAU", "XAGUSDT", "BZUSDT", "ONDO-USDT-SWAP", "XAU-USDT-SWAP"}}})
	got := fn("binance")
	want := []string{"XAU", "XAG", "BZ", "ONDO"}
	if len(got) != len(want) {
		t.Fatalf("universe 基名 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("universe 基名[%d] = %q, want %q（保序）", i, got[i], want[i])
		}
	}

	// 空见证集 → 空动态集（不 panic）
	if got := universeDepthBases(ctx, fakeDepthBaseLister{})("gate"); len(got) != 0 {
		t.Fatalf("空见证集应零动态基名，got %v", got)
	}
	// 分所隔离：binance 集不串 okx
	if got := fn("okx"); len(got) != 0 {
		t.Fatalf("分所精集：okx 不应见 binance 集，got %v", got)
	}
}

func TestD057_DepthBasesMerge(t *testing.T) {
	ctx := context.Background()
	static := len(exchange.DepthWhitelistBases)

	// nil accessor → 仅静态白名单
	c := &cexDepthCollector{}
	if got := len(c.bases("binance")); got != static {
		t.Fatalf("nil extraBases 应只有静态 %d 基名，got %d", static, got)
	}

	// 静态 ∪ universe 基名去重保序（XAU 与静态重名去重，universe 序在后）
	c2 := &cexDepthCollector{extraBases: universeDepthBases(ctx, fakeDepthBaseLister{rows: map[string][]string{"gate": {"XAUUSDT", "ONDO", "TRUMPUSDT"}}})}
	got := c2.bases("gate")
	if len(got) != static+2 { // ONDO + TRUMP 并入，XAU 去重
		t.Fatalf("合并后应为 %d 基名，got %v", static+2, got)
	}
	seen := map[string]bool{}
	for i, b := range got {
		if seen[b] {
			t.Fatalf("基名 %q 重复", b)
		}
		seen[b] = true
		if i < static && b != exchange.DepthWhitelistBases[i] {
			t.Fatalf("静态白名单保序破坏：bases[%d]=%q, want %q", i, b, exchange.DepthWhitelistBases[i])
		}
	}
	if got[static] != "ONDO" || got[static+1] != "TRUMP" {
		t.Fatalf("universe 基名应在静态后保序：%v", got[static:])
	}
}

func TestD057_DepthBasesUniverseReadFail(t *testing.T) {
	// universe 读失败 → 仅静态集（退化不阻断本轮）
	c := &cexDepthCollector{extraBases: universeDepthBases(context.Background(), fakeDepthBaseLister{err: errors.New("conn refused")})}
	got := c.bases("okx")
	if len(got) != len(exchange.DepthWhitelistBases) {
		t.Fatalf("读失败应退化仅静态 %d 基名，got %v", len(exchange.DepthWhitelistBases), got)
	}
	for i, b := range exchange.DepthWhitelistBases {
		if got[i] != b {
			t.Fatalf("静态保序破坏：bases[%d]=%q, want %q", i, got[i], b)
		}
	}
}
