// earn APR 判断引擎测试——审计修复点逐项覆盖（P0-2 单位/P2-1 方向/P2-3 优先级/P3-4 看空/N3 ProductType）。
// 逆回购基准（fin_repo_rates）随 cnfi/repo 面摘除——稳定币判断仅剩跨所对比，测试去 DB 依赖纯内存。
package cexscan

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
)

// judgeFixture 单用例输入：APR 行集 + 持仓。
type judgeFixture struct {
	name        string
	aprs        []pgstore.CexEarnAPR
	holdings    map[string]map[string]float64
	wantVerdict string   // 期望该行 verdict
	wantReason  []string // 期望 reasons 包含的子串（逐个 Contains）
}

func TestEarnJudge(t *testing.T) {
	ctx := context.Background()

	stable := func(venue, product string, apr float64) pgstore.CexEarnAPR {
		return pgstore.CexEarnAPR{Venue: venue, Ccy: "USDT", ProductType: product, APR: apr}
	}
	token := func(venue, ccy string, apr float64) pgstore.CexEarnAPR {
		return pgstore.CexEarnAPR{Venue: venue, Ccy: ccy, ProductType: "flexible", APR: apr}
	}

	cases := []judgeFixture{
		{
			name: "单所_无跨所_not_opportunity", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.035)},
			wantVerdict: "not_opportunity",
		},
		{
			// P2-1：diff=abs(0.035-0.024)=0.011 > 0.005 → 投放候选
			name: "P2-1_跨所_OKX高_投放候选", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.035), stable("binance", "flexible", 0.024)},
			wantVerdict: "opportunity", wantReason: []string{"跨所投放候选"},
		},
		{
			// P2-1 方向性：反向（BN 高）仍检出——固定 OKX-Binance 方向则恒 false
			name: "P2-1_跨所_反向_仍检出", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.024), stable("binance", "flexible", 0.035)},
			wantVerdict: "opportunity", wantReason: []string{"跨所投放候选"},
		},
		{
			// 跨所 diff=0.004 ≤ 0.005 成本阈值 → not_opportunity
			name: "P2-1_跨所_低于成本阈值_not_opportunity", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.035), stable("binance", "flexible", 0.031)},
			wantVerdict: "not_opportunity",
		},
		{
			name: "代币_持有_正APR_opportunity", aprs: []pgstore.CexEarnAPR{token("okx", "DOGE", 0.05)},
			holdings: map[string]map[string]float64{"okx": {"DOGE": 100}}, wantVerdict: "opportunity", wantReason: []string{"已持有，可存入"},
		},
		{
			name: "代币_不持有_not_opportunity", aprs: []pgstore.CexEarnAPR{token("okx", "DOGE", 0.05)},
			wantVerdict: "not_opportunity", wantReason: []string{"不持有，需买币"},
		},
		{
			// P3-4：不持有 + 高 APR → trap + "可能"措辞
			name: "P3-4_不持有_高APR_trap", aprs: []pgstore.CexEarnAPR{token("okx", "BETH", 0.6)},
			wantVerdict: "trap", wantReason: []string{"可能是做空需求信号"},
		},
		{
			// P3-4：持有 + 高 APR → 保持 opportunity + 注意 reason
			name: "P3-4_持有_高APR_opportunity加注", aprs: []pgstore.CexEarnAPR{token("okx", "BETH", 0.6)},
			holdings: map[string]map[string]float64{"okx": {"BETH": 2}}, wantVerdict: "opportunity", wantReason: []string{"高 APR，注意做空信号"},
		},
		{
			// 持币浮点阈值：1e-9 < 1e-8 → 不持有
			name: "代币_qty1e-9_低于阈值_不持有", aprs: []pgstore.CexEarnAPR{token("okx", "DOGE", 0.05)},
			holdings: map[string]map[string]float64{"okx": {"DOGE": 1e-9}}, wantVerdict: "not_opportunity",
		},
		{
			// N3：同 venue+ccy 不同 ProductType → 两条独立 judgment（ProductType 显式透传）
			name:        "N3_ProductType_透传_独立judgment",
			aprs:        []pgstore.CexEarnAPR{stable("okx", "flexible", 0.035), stable("okx", "demand", 0.035)},
			wantVerdict: "not_opportunity",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := NewEarnJudge(tc.holdings)
			got, err := j.Judge(ctx, tc.aprs)
			if err != nil {
				t.Fatalf("Judge: %v", err)
			}
			if tc.name == "N3_ProductType_透传_独立judgment" {
				if len(got) != 2 {
					t.Fatalf("want 2 judgments (同 venue+ccy 异 ProductType), got %d", len(got))
				}
				if got[0].ProductType == got[1].ProductType || got[0].ProductType == "" {
					t.Errorf("ProductType 未逐行透传: %q vs %q", got[0].ProductType, got[1].ProductType)
				}
				return
			}
			if len(got) != len(tc.aprs) {
				t.Fatalf("want %d judgments, got %d", len(tc.aprs), len(got))
			}
			g := got[0]
			if g.Verdict != tc.wantVerdict {
				t.Errorf("verdict want %q, got %q (reasons=%v)", tc.wantVerdict, g.Verdict, g.Reasons)
			}
			for _, want := range tc.wantReason {
				found := false
				for _, r := range g.Reasons {
					if strings.Contains(r, want) {
						found = true
					}
				}
				if !found {
					t.Errorf("reasons 缺 %q：got %v", want, g.Reasons)
				}
			}
			// ProductType 透传（单行用例）
			if len(tc.aprs) == 1 && g.ProductType != tc.aprs[0].ProductType {
				t.Errorf("ProductType want %q, got %q", tc.aprs[0].ProductType, g.ProductType)
			}
		})
	}
}
