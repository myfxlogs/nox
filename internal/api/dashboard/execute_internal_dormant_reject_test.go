package dashboard

import (
	"context"
	"strings"
	"testing"

	"nox/internal/store/pgstore"
)

// TestD209_ExecuteInternal_DormantReject 执行侧防御锚——XAU@okx 类休眠键命中→FAILED 带键；
// 键缺→越过休眠闸（落到后续闸的其他败因，不携休眠文案）。
func TestD209_ExecuteInternal_DormantReject(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	svc, oppID := d328SeedManualFixture(t, st) // CLUSDT gate:perp × 3001157:mt5
	ctx := context.Background()

	status, msg, _ := svc.ExecuteOpportunityInternal(ctx, oppID, "d209-t0", 1.0)
	if strings.Contains(msg, "休眠轨") {
		t.Fatalf("无休眠行不该拦: %s %s", status, msg)
	}

	one := 1.0
	if err := st.UpsertCapabilityConstraint(ctx, &pgstore.CapabilityConstraint{
		Scope: "bucket", Subject: "dormant:xasset_carry:CL@gate", Qualifier: "always", ValueNum: &one,
	}); err != nil {
		t.Fatalf("seed dormant: %v", err)
	}
	defer func() {
		cons, _ := st.ListCapabilityConstraints(ctx)
		for _, c := range cons {
			if c.Subject == "dormant:xasset_carry:CL@gate" {
				_ = st.DeleteCapabilityConstraint(ctx, c.ID)
			}
		}
	}()

	status, msg, posID := svc.ExecuteOpportunityInternal(ctx, oppID, "d209-t1", 1.0)
	if status != "FAILED" || posID != 0 || !strings.Contains(msg, "休眠轨（D-209）xasset_carry:CL@gate") {
		t.Fatalf("休眠命中应 FAILED 带键, got %s/%d %s", status, posID, msg)
	}
}
