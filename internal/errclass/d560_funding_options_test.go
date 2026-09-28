package errclass

// D-560 T3 锚：FundingOptionsText 矩阵——gate 拒文三选项实数断言（830/1000/0.8/5
// → ceil50(30)=50 / ceil(830×5÷800)=6 / 800×5=4000）；mt5 无杠杆项；非资金拒文/字段缺→""。
import (
	"strings"
	"testing"
)

func TestD560_FundingOptionsTextGate(t *testing.T) {
	msg := "gate margin insufficient: need 830.00 > avail 1000.00 × 0.80 (lev 5 mark 4350.12)"
	got := FundingOptionsText(msg)
	for _, want := range []string{"解法：", "向gate账户补可用资金 ≥$50", "杠杆 5× 调至 ≥6×", "单笔开仓金额降至 ≤$4000"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in %q", want, got)
		}
	}
}

func TestD560_FundingOptionsTextMT5(t *testing.T) {
	msg := "mt5 margin insufficient: required 500.00 > avail 400.00 (freeMargin 500.00 × 0.80)"
	got := FundingOptionsText(msg)
	if !strings.Contains(got, "向该账户补可用资金 ≥$200") || !strings.Contains(got, "单笔开仓数量调小") {
		t.Errorf("mt5 应出①③（无杠杆项）, got %q", got)
	}
	if strings.Contains(got, "杠杆") {
		t.Errorf("mt5 拒文无 lev 应无杠杆项, got %q", got)
	}
}

func TestD560_FundingOptionsTextOKX(t *testing.T) {
	msg := "okx margin insufficient: sz 1.5000 张 > avail 1.2000 张 × 0.80 (BTC-USDT-SWAP)"
	got := FundingOptionsText(msg)
	if !strings.Contains(got, "补可用资金") || !strings.Contains(got, "单笔开仓数量调小") {
		t.Errorf("okx 张数拒文应出①③文字形式, got %q", got)
	}
	if strings.Contains(got, "$") {
		t.Errorf("okx 无 $ 额不应出现金额, got %q", got)
	}
}

func TestD560_FundingOptionsTextNonFunding(t *testing.T) {
	for _, msg := range []string{
		"some other error",
		"gate margin insufficient: need > avail (lev)", // 字段缺 → ""
		"quote venue symbol XAUUSD247m != primary",     // variant 拒文非资金拒
	} {
		if got := FundingOptionsText(msg); got != "" {
			t.Errorf("msg=%q 应返空, got %q", msg, got)
		}
	}
}

// IsFundingRejectMsg 判据面（autotrade marginWarnThrottled 同调单源）。
func TestD560_IsFundingRejectMsg(t *testing.T) {
	if !IsFundingRejectMsg("GATE MARGIN INSUFFICIENT: need 1 > avail 0") {
		t.Error("大小写不敏感命中应真")
	}
	if !IsFundingRejectMsg("liq-distance 0.02 < 0.05") {
		t.Error("强平闸同归资金类")
	}
	if IsFundingRejectMsg("MARKET_CLOSED") {
		t.Error("非资金拒文应否")
	}
}
