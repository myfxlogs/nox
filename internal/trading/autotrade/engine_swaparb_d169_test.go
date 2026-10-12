package autotrade

import "testing"

// 单测：入场报价合理性门禁纯函数（先红后绿）。
// 场景锚：posID=470（FxPro×PXBT BRENT 290 bps 虚假价差）、posID=469（CMC UKOIL 盲腿）。

func TestEntryBasisGateCheck(t *testing.T) {
	cases := []struct {
		name           string
		basisBps       float64
		basisAvailable bool
		maxBps         float64
		wantReject     bool
		wantReason     string
	}{
		{"maxBps=0 不校验（向后兼容）", 290, true, 0, false, ""},
		{"盲腿 fail-closed（469 场景）", 0, false, 30, true, "blind leg"},
		{"basisBps=290 超限（470 场景复现）", 290, true, 30, true, "divergence"},
		{"basisBps=5 正常放行", 5, true, 30, false, ""},
		{"basisBps=-50 负向基差偏离拦截", -50, true, 30, true, "divergence"},
	}
	for _, c := range cases {
		gotReject, gotReason := entryBasisGateCheck(c.basisBps, c.basisAvailable, c.maxBps)
		if gotReject != c.wantReject {
			t.Errorf("%s: reject=%v, want %v", c.name, gotReject, c.wantReject)
		}
		if gotReason != c.wantReason {
			t.Errorf("%s: reason=%q, want %q", c.name, gotReason, c.wantReason)
		}
	}
}

func TestEntrySpreadMaxGateCheck(t *testing.T) {
	cases := []struct {
		name       string
		spreadBps  float64
		maxBps     float64
		wantReject bool
		wantReason string
	}{
		{"maxBps=0 不校验（向后兼容）", 290, 0, false, ""},
		{"spreadBps=290 超限（470 场景复现）", 290, 30, true, "too good"},
		{"spreadBps=5 正常放行", 5, 30, false, ""},
		{"spreadBps=-5 负值=开仓成本不拦截", -5, 30, false, ""},
	}
	for _, c := range cases {
		gotReject, gotReason := entrySpreadMaxGateCheck(c.spreadBps, c.maxBps)
		if gotReject != c.wantReject {
			t.Errorf("%s: reject=%v, want %v", c.name, gotReject, c.wantReject)
		}
		if gotReason != c.wantReason {
			t.Errorf("%s: reason=%q, want %q", c.name, gotReason, c.wantReason)
		}
	}
}
