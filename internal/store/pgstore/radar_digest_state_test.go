package pgstore

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
)

// D-389/D-392 锚 C：migration 双注册实证——schema_migrations 有 123/124 +
// 两表可选 + 成员访问器往返（漏注册则表不存在）。需 NOX_TEST_PG_DSN，未设置时 skip。
func TestD389_RadarDigestState(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	for _, v := range []int{123, 124} {
		var ok bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)`, v).Scan(&ok); err != nil {
			t.Fatalf("schema_migrations 查询: %v", err)
		}
		if !ok {
			t.Fatalf("schema_migrations 无 version=%d（embed/migrate 双注册缺一）", v)
		}
	}
	// 成员表往返：apply(cur={P},pushed={P}) → Get 回读 in_prev+pushed 戳；
	// 次轮 cur 空集 → in_prev 清零；过期清理 NULL 分支。
	now := time.Now()
	cur := map[string][]string{"d392_probe": {"P"}}
	if err := s.ApplyRadarDigestRun(ctx, cur, map[string][]string{"d392_probe": {"P"}}, now); err != nil {
		t.Fatalf("ApplyRadarDigestRun: %v", err)
	}
	m, err := s.GetRadarDigestMembers(ctx)
	if err != nil {
		t.Fatalf("GetRadarDigestMembers: %v", err)
	}
	st, ok := m["d392_probe"]["P"]
	if !ok || !st.InPrevSet || st.LastPushedAt == nil {
		t.Fatalf("往返失败: %+v ok=%v", st, ok)
	}
	if err := s.ApplyRadarDigestRun(ctx, map[string][]string{"d392_probe": nil}, nil, now.Add(time.Hour)); err != nil {
		t.Fatalf("空集轮 apply: %v", err)
	}
	m, _ = s.GetRadarDigestMembers(ctx)
	if m["d392_probe"]["P"].InPrevSet {
		t.Fatal("空集轮 in_prev_set 应清零")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM radar_digest_member WHERE kind='d392_probe'`); err != nil {
		t.Fatalf("清理探针行: %v", err)
	}
}
