package pgstore

import (
	"context"
	"testing"
)

// TestAlertLevelDomain 级域断言锚：alerts CHECK 约束域=info/warn/critical——
// 错级（error）插应被拒（symdig llm.go:111 错级病灶回归锁；fakeStore 收
// 任意级的假绿面用真约束锚钉死）。
func TestAlertLevelDomain(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	defer s.pool.Exec(ctx, `DELETE FROM alerts WHERE message LIKE 't-level-domain%'`)
	if err := s.InsertAlert(ctx, nil, "critical", "t-level-domain-critical"); err != nil {
		t.Fatalf("critical 合法级应过: %v", err)
	}
	if err := s.InsertAlert(ctx, nil, "error", "t-level-domain-error"); err == nil {
		t.Fatal("error 级应被 CHECK 约束拒（级域=info/warn/critical）")
	}
}
