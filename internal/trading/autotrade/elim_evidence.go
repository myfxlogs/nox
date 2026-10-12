package autotrade

import (
	"context"
	"log"

	"nox/internal/review/mmm"
)

// elimEvidenceSuffix D-180 S①：判死 reason 追加 B.12 证据字段（置信下界+距宣判笔数）。
// 纯证据非判据——disable 条件零改动；Zσ 走 app_config NOX_ELIM_ZSIGMA 热读（默认 2）。
func (e *Engine) elimEvidenceSuffix(ctx context.Context, total int, winRate float64) string {
	z := 2.0
	if v, ok, err := e.store.AppConfigFloat(ctx, "NOX_ELIM_ZSIGMA"); err != nil {
		log.Printf("[autotrade] elim z-sigma config: %v（回落默认 2）", err)
	} else if ok && v > 0 {
		z = v
	}
	return mmm.ElimEvidence(total, winRate, 0.30, z)
}
