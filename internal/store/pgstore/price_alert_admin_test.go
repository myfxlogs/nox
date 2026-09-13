package pgstore

import (
	"context"
	"testing"
)

// TestPriceAlertAdminCRUD 价格提醒规则管理面全生命周期：Create→List→Update（启停）→Delete（D-292 S2）。
func TestPriceAlertAdminCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE price_alerts RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// 1. Create：新建启用规则
	r1, err := s.CreatePriceAlertRule(ctx, "TRXUSDT", ">=", 0.34, "warn", "TRX 涨到 0.34，补仓机会")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r1.ID <= 0 {
		t.Fatalf("id 未回填：%d", r1.ID)
	}
	if !r1.Enabled {
		t.Fatalf("新建规则 enabled 应默认 true，got false")
	}
	if r1.LastAlertAt != nil {
		t.Fatalf("新建规则 last_alert_at 应 nil，got %v", r1.LastAlertAt)
	}

	// 2. Create 第二条（disabled 后用于排序断言）
	r2, err := s.CreatePriceAlertRule(ctx, "BTCUSDT", "<=", 100000, "critical", "BTC 跌破 10 万")
	if err != nil {
		t.Fatalf("Create r2: %v", err)
	}

	// 3. List：启用规则置顶（enabled DESC），同状态按 id ASC
	list, err := s.ListPriceAlertRules(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("期望 2 条，got %d", len(list))
	}
	if !list[0].Enabled || !list[1].Enabled {
		t.Fatalf("两条均应 enabled=true（新建默认启用），got %+v", list)
	}
	if list[0].ID != r1.ID || list[1].ID != r2.ID {
		t.Fatalf("顺序应为 id ASC，got %d,%d", list[0].ID, list[1].ID)
	}

	// 4. Update：停用 r1 + 改阈值
	upd, err := s.UpdatePriceAlertRule(ctx, r1.ID, "TRXUSDT", ">=", 0.40, "warn", "TRX 涨到 0.40", false)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if upd.Enabled {
		t.Fatalf("停用后 enabled 应 false")
	}
	if upd.Threshold != 0.40 {
		t.Fatalf("阈值应更新为 0.40，got %v", upd.Threshold)
	}

	// 5. List 排序：启用置顶 → r2（enabled）在前，r1（disabled）在后
	list2, err := s.ListPriceAlertRules(ctx)
	if err != nil {
		t.Fatalf("List2: %v", err)
	}
	if list2[0].ID != r2.ID || list2[1].ID != r1.ID {
		t.Fatalf("停用后排序应为 r2(启用)→r1(停用)，got %d,%d", list2[0].ID, list2[1].ID)
	}

	// 6. Delete：硬删 r1
	deleted, err := s.DeletePriceAlertRule(ctx, r1.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted {
		t.Fatalf("Delete 应返回 true（行存在）")
	}
	list3, _ := s.ListPriceAlertRules(ctx)
	if len(list3) != 1 || list3[0].ID != r2.ID {
		t.Fatalf("删除后应剩 r2，got %+v", list3)
	}

	// 7. Delete 不存在的 id → false
	deleted2, _ := s.DeletePriceAlertRule(ctx, 999999)
	if deleted2 {
		t.Fatalf("Delete 不存在 id 应返回 false")
	}
}
