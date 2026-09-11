// D-273 S3：earn APR 判断引擎测试——审计修复点逐项覆盖（P0-2 单位/P2-1 方向/P2-3 优先级/P3-4 看空/N3 ProductType）。
// DB 依赖：RepoRate30dAvg 基准（fin_repo_rates）——经 testutil.TestDSN 门禁（禁生产 DSN）。
package cexscan

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// judgeFixture 单用例输入：APR 行集 + 持仓 + 是否播种逆回购基准（204007 rate_pct=4.5 百分点）。
type judgeFixture struct {
	name        string
	aprs        []pgstore.CexEarnAPR
	holdings    map[string]map[string]float64
	seedBench   bool
	wantVerdict string   // 期望该行 verdict
	wantReason  []string // 期望 reasons 包含的子串（逐个 Contains）
}

func TestEarnJudge(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	cleanup := func() {
		_, _ = st.Pool().Exec(ctx, "DELETE FROM fin_repo_rates WHERE code='204007'")
	}
	cleanup()
	t.Cleanup(cleanup)

	bench := func(t *testing.T) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO fin_repo_rates (code, name, rate_pct, trade_date) VALUES ('204007','GC007',4.5,current_date)`); err != nil {
			t.Fatalf("seed fin_repo_rates: %v", err)
		}
	}

	stable := func(venue, product string, apr float64) pgstore.CexEarnAPR {
		return pgstore.CexEarnAPR{Venue: venue, Ccy: "USDT", ProductType: product, APR: apr}
	}
	token := func(venue, ccy string, apr float64) pgstore.CexEarnAPR {
		return pgstore.CexEarnAPR{Venue: venue, Ccy: ccy, ProductType: "flexible", APR: apr}
	}

	cases := []judgeFixture{
		{
			// P0-2 单位锚：earn APR 小数 0.045 vs 基准百分点 4.5（/100 后相等）→ 优于（写反则恒判不如）
			name: "P0-2_单位_APR0.045_vs_基准4.5百分点_优于", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.045)},
			seedBench: true, wantVerdict: "opportunity", wantReason: []string{"优于逆回购", "flexible 不锁定"},
		},
		{
			name: "P0-2_单位_APR0.035_vs_基准4.5百分点_不如", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.035)},
			seedBench: true, wantVerdict: "not_opportunity", wantReason: []string{"不如逆回购"},
		},
		{
			name: "P2-3_无基准_无跨所_not_opportunity", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.035)},
			seedBench: false, wantVerdict: "not_opportunity",
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
			// P2-3 优先级表：不如基准 + 跨所投放候选 → opportunity（跨所更优）
			name: "P2-3_不如基准加投放候选_opportunity", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.035), stable("binance", "flexible", 0.024)},
			seedBench: true, wantVerdict: "opportunity", wantReason: []string{"不如逆回购", "跨所投放候选"},
		},
		{
			// P2-3 优先级表：不如基准 + 跨所无意义 → not_opportunity
			name: "P2-3_不如基准加无意义_not_opportunity", aprs: []pgstore.CexEarnAPR{stable("okx", "flexible", 0.035)},
			seedBench: true, wantVerdict: "not_opportunity",
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
			cleanup()
			if tc.seedBench {
				bench(t)
			}
			j := NewEarnJudge(st, tc.holdings)
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
