// paper_cex_step_test.go —— D-051 R1：StepPaperCexCarryOpen+AccrueExit 一步全
// pass 测试合成件（生产两函数解耦分调——hook=Open、30s 轮=AccrueExit；既有
// 测试语义=一轮完整步进，免逐调用点改造）。
package scan

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// StepPaperCexCarry 测试用全 pass（Open→AccrueExit 顺序同原合成函数）。
func StepPaperCexCarry(ctx context.Context, st PaperStore, cands []CexPaperCand, now time.Time, opts PaperCexOpts, flags map[string]pgstore.VenueSymbolFlag, quoteAlive func(venueKey, symbol string) (time.Time, bool)) (opened []string, exited int, err error) {
	opened, _, err = StepPaperCexCarryOpen(ctx, st, cands, now, opts, flags, quoteAlive)
	if err != nil {
		return opened, 0, err
	}
	exited, _, err = StepPaperCexCarryAccrueExit(ctx, st, cands, now)
	return opened, exited, err
}
