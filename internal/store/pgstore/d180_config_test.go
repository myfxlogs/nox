package pgstore

import (
	"context"
	"testing"
)

// TestD180_AppConfigSeed：迁移 186 播种键热读（ARB_ELIM_ZSIGMA/ARB_RETRO_DEP_Z）。
func TestD180_AppConfigSeed(t *testing.T) {
	s := sharedTestStore(t)
	for _, k := range []string{"ARB_ELIM_ZSIGMA", "ARB_RETRO_DEP_Z"} {
		v, ok, err := s.AppConfigFloat(context.Background(), k)
		if err != nil || !ok || v != 2.0 {
			t.Fatalf("app_config %s: v=%v ok=%v err=%v（迁移 186 应播种 2）", k, v, ok, err)
		}
	}
}
