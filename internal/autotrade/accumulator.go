package autotrade

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// comboAccumulator 当日 combo 累积器（D-080 S4 write-behind）。
// 每 loop 独立实例；当日累计 Σ/count/min/max；flush 写累计值直接覆盖；
// flush 后不清零，UTC 日切重置。严禁增量合并 avg。
type comboAccumulator struct {
	day     time.Time // UTC 日期（截断到日）
	entries map[string]*comboAccumEntry
}

type comboAccumEntry struct {
	comboKey string
	typ      string
	sum      float64
	count    int
	min      float64
	max      float64
	firstAt  time.Time
	lastAt   time.Time
}

// newComboAccumulator 创建累积器。
func newComboAccumulator() *comboAccumulator {
	return &comboAccumulator{
		entries: make(map[string]*comboAccumEntry),
	}
}

// Record 喂入一个样本（全组合含低于门槛）。
// 日切时自动重置（UTC 日切开新一日）。
func (a *comboAccumulator) Record(comboKey, typ string, netBps float64, ts time.Time) {
	utcDay := ts.UTC().Truncate(24 * time.Hour)
	if !utcDay.Equal(a.day) {
		a.day = utcDay
		a.entries = make(map[string]*comboAccumEntry)
	}
	e, ok := a.entries[comboKey]
	if !ok {
		e = &comboAccumEntry{
			comboKey: comboKey,
			typ:      typ,
			min:      netBps,
			max:      netBps,
			firstAt:  ts,
		}
		a.entries[comboKey] = e
	}
	e.sum += netBps
	e.count++
	if netBps < e.min {
		e.min = netBps
	}
	if netBps > e.max {
		e.max = netBps
	}
	e.lastAt = ts
}

// Flush 批量 upsert 累积值到 DB（覆盖语义：后写覆盖=单调完善）。
// flush 后不清零（UTC 日切 Record 内重置）。
func (a *comboAccumulator) Flush(ctx context.Context, store *pgstore.Store) error {
	if len(a.entries) == 0 {
		return nil
	}
	rows := make([]pgstore.ComboDailyRow, 0, len(a.entries))
	for _, e := range a.entries {
		netAvg := e.sum / float64(e.count)
		var annualAvg *float64
		if e.typ == "FUNDING_HEDGE" || e.typ == "PERP_FUNDING" {
			v := netAvg
			annualAvg = &v
		}
		rows = append(rows, pgstore.ComboDailyRow{
			ComboKey:  e.comboKey,
			Day:       a.day,
			Type:      e.typ,
			Samples:   e.count,
			NetAvg:    netAvg,
			NetMin:    e.min,
			NetMax:    e.max,
			AnnualAvg: annualAvg,
			FirstAt:   e.firstAt,
			LastAt:    e.lastAt,
		})
	}
	return store.UpsertComboDaily(ctx, rows)
}
