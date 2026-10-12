// D-175 锚：app_config 登记态往返——Defer→List 携字段→Clear 幂等。
// 重启不丢锚：态在 store 非内存（跨 Defer/List 无共享结构体，直读落库文档）。
package pgstore

import (
	"context"
	"testing"
)

func TestD175_LeggedDeferralRoundtrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM app_config WHERE key='ARB_LEGGED_DEFERRALS'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if err := s.DeferLeggedAlert(ctx, 508, 1, 2); err != nil {
		t.Fatalf("defer: %v", err)
	}
	list, err := s.ListLeggedDeferrals(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].PosID != 508 || list[0].Filled != 1 || list[0].Total != 2 || list[0].Since.IsZero() {
		t.Fatalf("登记应携 posID/filled/total/since, got %+v", list)
	}
	if err := s.ClearLeggedDeferral(ctx, 508); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := s.ClearLeggedDeferral(ctx, 508); err != nil {
		t.Fatalf("重复 clear 应幂等: %v", err)
	}
	list, err = s.ListLeggedDeferrals(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("销键后应空, got %+v err=%v", list, err)
	}
}
