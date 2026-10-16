package dashboard

// position_group_test.go —— D-577 归组键 RPC 透出断言（复审①硬伤锚：
// store 层读到列 ≠ RPC 面透出）。positionToProto：有键透出值；NULL→
// unassigned=true（前端禁猜挂，如实落未归组格）。

import (
	"context"
	"testing"

	"nox/internal/store/pgstore"
)

func TestPositionToProto_GroupKey(t *testing.T) {
	ctx := context.Background()
	svc := &Service{}
	k := "funding_carry"
	with := &pgstore.Position{ID: 1, Type: "CARRY", StrategyKey: &k}
	pb := svc.positionToProto(ctx, with)
	if pb.StrategyKey == nil || *pb.StrategyKey != "funding_carry" || pb.Unassigned {
		t.Fatalf("有键应透出且 unassigned=false: %+v", pb)
	}
	without := &pgstore.Position{ID: 2, Type: "PROBE"}
	pb2 := svc.positionToProto(ctx, without)
	if pb2.StrategyKey != nil || !pb2.Unassigned {
		t.Fatalf("NULL 键应 unassigned=true: %+v", pb2)
	}
}
