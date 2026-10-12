// margin_alert_text_test.go —— D-556 验收锚：
// T1 七类拒文矩阵（账户名+关键金额+「请加保证金」前缀）／T2 未知形态泛化文案／
// T3 非资金拒文零告警／T4 节流 venue 键（同 combo+venue 单条、换 venue 新条）／
// T5 闸级资金拒走 warn、Src 闸仍 info。
package autotrade

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
)

// aliasStub 闭包（gate 测试注入形态）。
func aliasStub(vk string) string {
	if vk == "3001157:mt5" {
		return "主金账户"
	}
	return ""
}

// TestT1_RejectMatrix T1：七类拒文各断言关键要素。
func TestT1_RejectMatrix(t *testing.T) {
	cases := []struct {
		name    string
		intent  TradeIntent
		msg     string
		wantSub []string
	}{
		{
			name:   "gate margin insufficient",
			intent: TradeIntent{Canonical: "XAUUSD", Strategy: "XASSET_CARRY", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"},
			msg:    "gate margin insufficient: need 832.38 > avail 1000.00 × 0.80 (lev 20 mark 2651.5)",
			wantSub: []string{"【请加保证金】XAUUSD", "（XASSET_CARRY）", "gate·永续账户", "可用$1000",
				"需保证金$832", "可动用上限$800", "还差约$32", "转入USDT", "gate margin insufficient"},
		},
		{
			name:   "binance margin insufficient",
			intent: TradeIntent{Canonical: "BTCUSDT", Strategy: "FUNDING_HEDGE", VenueBuy: "binance:spot", VenueSell: "binance:perp"},
			msg:    "binance margin insufficient: need 250.00 > avail 400.00 × 0.80 (lev 10 mark 96000)",
			// 同所双腿同前缀——拒文无腿型区分度，账户名透出交易所名不猜腿型（实现语义钉）。
			wantSub: []string{"binance 交易账户", "可用$400", "还差约$0", "请向binance 交易账户转入USDT"},
		},
		{
			name:    "okx margin sz",
			intent:  TradeIntent{Canonical: "BTCUSD", Strategy: "XASSET_CARRY", VenueSell: "okx:perp"},
			msg:     "okx margin insufficient: sz 1.5000 张 > avail 0.8000 张 × 0.95 (liq px …)",
			wantSub: []string{"OKX 交易账户保证金不足", "需 1.5000 张", "只能开 0.8000 张", "补充 USDT"},
		},
		{
			name:    "spot cash insufficient",
			intent:  TradeIntent{Canonical: "SKLUSDT", Strategy: "FUNDING_HEDGE", VenueBuy: "gate:spot", VenueSell: "gate:perp"},
			msg:     "gate spot cash insufficient: need 1200.00 USDT > avail 900.00 × 0.98 (qty …)",
			wantSub: []string{"gate·现货账户里买币的钱不够", "需要 1200.00 USDT", "可用 900.00", "充值 USDT"},
		},
		{
			name:    "mt5 margin insufficient alias",
			intent:  TradeIntent{Canonical: "XAUUSD", Strategy: "XASSET_CARRY", VenueBuy: "3001157:mt5", VenueSell: "okx:perp"},
			msg:     "mt5 margin insufficient: required 450.25 > avail 380.10 (freeMargin 380.10 × 0.90)",
			wantSub: []string{"MT5 账户 主金账户", "本次需 450.25", "可用 380.10", "向该 MT5 账户入金"},
		},
		{
			name:    "liq-distance est",
			intent:  TradeIntent{Canonical: "BTCUSD", Strategy: "XASSET_CARRY", VenueBuy: "3001157:mt5"},
			msg:     "3001157 liq-distance est=0.0042 < floor=0.0100 (isolated lev=20 mmr=0.005)",
			wantSub: []string{"离爆仓线太近", "约 0.0042%", "要求≥0.0100%", "补充保证金"},
		},
		{
			name:    "liq-distance cross",
			intent:  TradeIntent{Canonical: "BTCUSDT", Strategy: "SWAPARB", VenueSell: "binance:perp"},
			msg:     "binance liq-distance cross notional/equity=1.350 > cap=0.900 (notional=…)",
			wantSub: []string{"名义金额是账户权益的 1.350 倍", "上限 0.900 倍", "补充权益资金"},
		},
	}
	for _, c := range cases {
		out := fundingAlertText(c.intent, c.msg, aliasStub)
		for _, w := range c.wantSub {
			if !strings.Contains(out, w) {
				t.Errorf("%s: 输出缺 %q\nout=%s", c.name, w, out)
			}
		}
		for _, banned := range []string{"margin precheck", "leg "} {
			if strings.Contains(out, banned) && !strings.Contains(out, "技术明细：") {
				t.Errorf("%s: 白话正文禁黑话 %q", c.name, banned)
			}
		}
	}
}

// TestT2_UnknownFundingGeneric T2：未知形态 funding 拒文→泛化文案+原文明细保留。
func TestT2_UnknownFundingGeneric(t *testing.T) {
	intent := TradeIntent{Canonical: "XYZ", Strategy: "SWAPARB", VenueSell: "gate:perp"}
	msg := "gate margin insufficient: some new format 42"
	out := fundingAlertText(intent, msg, aliasStub)
	if !strings.Contains(out, "【请加保证金】XYZ") || !strings.Contains(out, "gate·永续账户保证金不足") {
		t.Fatalf("泛化文案缺: %s", out)
	}
	if !strings.Contains(out, "some new format 42") {
		t.Fatalf("原文明细应保留: %s", out)
	}
}

// TestT3_NonFundingSilent T3：非资金拒文 → 空串（零告警）。
func TestT3_NonFundingSilent(t *testing.T) {
	intent := TradeIntent{Canonical: "X", Strategy: "S"}
	if out := fundingAlertText(intent, "price stale beyond freshness window", aliasStub); out != "" {
		t.Fatalf("非资金拒文应返回空串, got %s", out)
	}
}

// TestT4_ThrottleVenueKey T4：同 combo+venue 6h 单条；换 venue 再拒→新 warn。
func TestT4_ThrottleVenueKey(t *testing.T) {
	g, st := marginAlertGate(t)
	ctx := context.Background()
	intent := TradeIntent{Canonical: "XAUUSD", Strategy: "XASSET_CARRY", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"}
	msg := "gate margin insufficient: need 800 > avail 100 × 0.80"
	g.marginWarnThrottled(ctx, intent, msg)
	g.marginWarnThrottled(ctx, intent, msg) // 同键节流
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '%请加保证金%'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("同 combo+venue 6h 应单条, got %d", n)
	}
	// 换 venue 腿（okx 拒文）→ 新 warn。
	g.marginWarnThrottled(ctx, intent, "okx margin insufficient: sz 2 张 > avail 1 张 × 0.95")
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '%请加保证金%'`).Scan(&n); err != nil {
		t.Fatalf("count2: %v", err)
	}
	if n != 2 {
		t.Fatalf("换 venue 应新条, got %d", n)
	}
}

// TestT5_GateWarnAndSrcStaysInfo T5：闸级资金不足→warn（非 info）；Src 闸→仍 info。
func TestT5_GateWarnAndSrcStaysInfo(t *testing.T) {
	s := setupGateAlertStore(t)
	ctx := context.Background()
	g := NewGate(s, Config{}, NewControlPlane(s))
	fake := &fakeMarginFloor{}
	g.SetMarginFloorChecker(fake)
	// 无 capView（nil=Src 闸跳过）→资金不足判定直落（fake 恒过——本锚走真实拒：换计数 fake 拒）。
	deny := &denyOnceFloor{}
	g.SetMarginFloorChecker(deny)
	v := g.marginFloorGateCheck(ctx, TradeIntent{Canonical: "X", Strategy: "S", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"})
	if v.Pass {
		t.Fatal("资金不足应拒")
	}
	var warns, infos int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '%请加保证金%'`).Scan(&warns); err != nil {
		t.Fatalf("warns: %v", err)
	}
	if warns != 1 {
		t.Fatalf("闸级资金不足应产 1 warn, got %d", warns)
	}
	_ = infos
	// Src 闸仍 info：注入 nil-fail capView 走 Src 拒（活账本空+QuerierFor 缺→snap 源→Src 闸拒）。
	// capView 为 nil 时 Src 闸跳过——用 d549 既有锚覆盖 Src info 语义（此处只断言无新 info）。
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='info' AND message LIKE '%资金源非实拉%'`).Scan(&infos); err != nil {
		t.Fatalf("infos: %v", err)
	}
	if infos != 0 {
		t.Fatalf("本锚不应产 Src info（nil capView 跳过）, got %d", infos)
	}
}

// denyOnceFloor 恒拒 checker（T5 专用）。
type denyOnceFloor struct{}

func (d *denyOnceFloor) MarginFloorCheck(ctx context.Context, venueKey string, addNotionalUSD float64) (bool, string) {
	return false, "可用保证金不足（测试拒）"
}

// marginAlertGate DSN gate（真实 store 落库断言）。
func marginAlertGate(t *testing.T) (*Gate, *pgstore.Store) {
	t.Helper()
	s := setupGateAlertStore(t)
	g := NewGate(s, Config{}, NewControlPlane(s))
	g.SetVenueAlias(aliasStub)
	return g, s
}
