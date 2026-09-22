package pgstore

import (
	"context"
	"testing"

	"arb/internal/testutil"
)

// D-389 锚 C：migration 0123 双注册实证——schema_migrations 有 123 +
// radar_digest_state 表可选 + Get/Upsert 往返（漏注册则表不存在）。
// 需 ARB_TEST_PG_DSN，未设置时 skip。
func TestD389_RadarDigestState(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	var v123 bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=123)`).Scan(&v123); err != nil {
		t.Fatalf("schema_migrations 查询: %v", err)
	}
	if !v123 {
		t.Fatal("schema_migrations 无 version=123（embed/migrate 双注册缺一）")
	}
	// 表可选 + 方法往返
	if _, err := s.GetRadarDigests(ctx); err != nil {
		t.Fatalf("GetRadarDigests: %v", err)
	}
	if err := s.UpsertRadarDigest(ctx, "d389_test_probe", "h0"); err != nil {
		t.Fatalf("UpsertRadarDigest: %v", err)
	}
	m, err := s.GetRadarDigests(ctx)
	if err != nil || m["d389_test_probe"] != "h0" {
		t.Fatalf("往返失败: %v %v", m, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM radar_digest_state WHERE kind='d389_test_probe'`); err != nil {
		t.Fatalf("清理探针行: %v", err)
	}
}
