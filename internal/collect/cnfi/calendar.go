package cnfi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"arb/internal/store/pgstore"
)

// 2026 年中国市场日历种子（种子机制；数据=国务院办公厅《关于2026年部分节假日安排的通知》
// 国办发明电〔2025〕7号，2025-11-04 公布——[披露]公开数据，可入 git）。
// 更新责任=研究会话：2026-11 前须以 2027 年通知更新本表（D-232/F4 机制）。
const calendar2026Version = "guobanfa-2025-7"

// holidayRanges2026 放假日（含搭接周末，无害——周末本就非营业日）。
var holidayRanges2026 = [][2]string{
	{"2026-01-01", "2026-01-03"}, // 元旦
	{"2026-02-15", "2026-02-23"}, // 春节
	{"2026-04-04", "2026-04-06"}, // 清明
	{"2026-05-01", "2026-05-05"}, // 劳动节
	{"2026-06-19", "2026-06-21"}, // 端午
	{"2026-09-25", "2026-09-27"}, // 中秋
	{"2026-10-01", "2026-10-07"}, // 国庆
}

// makeupWorkdays2026 调休上班日（周末补班=营业日）。
var makeupWorkdays2026 = []string{
	"2026-01-04", "2026-02-14", "2026-02-28", "2026-05-09", "2026-09-20", "2026-10-10",
}

// SeedCalendar2026 upserts every day of 2026 (idempotent; 248 business days expected).
func SeedCalendar2026(ctx context.Context, st *pgstore.Store) (business, total int, err error) {
	holiday := map[string]bool{}
	for _, r := range holidayRanges2026 {
		d0, e0 := time.Parse("2006-01-02", r[0])
		d1, e1 := time.Parse("2006-01-02", r[1])
		if e0 != nil || e1 != nil {
			return 0, 0, fmt.Errorf("holiday range %v: %w", r, e0)
		}
		for d := d0; !d.After(d1); d = d.AddDate(0, 0, 1) {
			holiday[d.Format("2006-01-02")] = true
		}
	}
	makeup := map[string]bool{}
	for _, m := range makeupWorkdays2026 {
		makeup[m] = true
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 365; i++ {
		day := start.AddDate(0, 0, i)
		key := day.Format("2006-01-02")
		wd := day.Weekday()
		isBusiness := (wd != time.Saturday && wd != time.Sunday) || makeup[key]
		isBusiness = isBusiness && !holiday[key]
		if err := st.UpsertCNFICalendarDay(ctx, day, isBusiness, calendar2026Version); err != nil {
			return business, total, err
		}
		total++
		if isBusiness {
			business++
		}
	}
	return business, total, nil
}

// SeedLayerTargets writes the four-layer target ratio (纯比例可入 git，D-047 合规).
// evidence_ref 指向首份研究纪要（阈值证据底座）。
func SeedLayerTargets(ctx context.Context, st *pgstore.Store) error {
	v, _ := json.Marshal(map[string]float64{
		"liquidity": 20, "coupon": 50, "locked": 15, "opportunity": 15,
	})
	if err := st.UpsertCNFISignalConfigRaw(ctx, "layer_targets", v,
		"ZCode(D-226/D-242)", "docs/cnfi/研究/cnfi-市场快照-2026-09-05-ZCode.md"); err != nil {
		return err
	}
	// 预期年化%（公开市场参数 [媒体·研究纪要]，月度复盘校准）：流动=存单指数 1.4 /
	// 票息=中短债 2.0 / 锁定=储蓄国债 1.63 [披露·票面] / 机会=二永债基 2.2 [推断]。
	e, _ := json.Marshal(map[string]float64{
		"liquidity": 1.4, "coupon": 2.0, "locked": 1.63, "opportunity": 2.2,
	})
	return st.UpsertCNFISignalConfigRaw(ctx, "layer_expected_annual", e,
		"ZCode(D-242)", "docs/cnfi/研究/cnfi-市场快照-2026-09-05-ZCode.md")
}
