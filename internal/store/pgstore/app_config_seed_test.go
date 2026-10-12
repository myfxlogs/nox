package pgstore

import (
	"context"
	"testing"

	"nox/migrations"
)

// TestD180_AppConfigSeed：迁移 186 播种键热读（NOX_ELIM_ZSIGMA/NOX_RETRO_DEP_Z）。
func TestD180_AppConfigSeed(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	// 先重放播种（同进程其他测试可能 TRUNCATE app_config——迁移幂等可重入）
	if _, err := s.Pool().Exec(ctx, migrations.ElimEvidenceSeedSQL); err != nil {
		t.Fatalf("seed replay: %v", err)
	}
	for _, k := range []string{"NOX_ELIM_ZSIGMA", "NOX_RETRO_DEP_Z"} {
		v, ok, err := s.AppConfigFloat(context.Background(), k)
		if err != nil || !ok || v != 2.0 {
			t.Fatalf("app_config %s: v=%v ok=%v err=%v（迁移 186 应播种 2）", k, v, ok, err)
		}
	}
}
