package oppmatch

import (
	"testing"

	"nox/internal/store/pgstore"
)

// TestCredMT5_TypeKey 端到端锚：mt5 凭据行 → type:mt5 键命中 →
// credential:mt5 条件满足（cfd_swap_carry 请求箱误报复现径的反面）。
func TestCredMT5_TypeKey(t *testing.T) {
	v := ledgerView(&pgstore.CapabilityLedger{Credentials: []pgstore.VenueCredentialCap{
		{Venue: "3001157", VenueType: "mt5", Tags: []string{"cfd"}},
	}})
	if !v.Credentials["3001157"] || !v.Credentials["3001157:mt5"] || !v.Credentials["type:mt5"] {
		t.Fatalf("credentials=%v, want venue/venue:type/type:type 三级键", v.Credentials)
	}
	if !EvalCond(v, Cond{Kind: "credential", Subject: "mt5"}) {
		t.Error("credential:mt5 未命中——归一病灶复现")
	}
	// type:cfd 兼容保——tags 不入键，历史无 subject:cfd 消费面，如实不命中
	if v.Credentials["type:cfd"] {
		t.Error("type:cfd 不应注册（venue_type 真值径）")
	}
}
