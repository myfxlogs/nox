package autotrade

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// comboAccumulator 当日 combo 累积器（D-080 S4 write-behind，R2 重构）。
// 每 loop 独立实例（无共享=无竞态）；当日累计 Σ/count/min/max；flush 写累计值直接覆盖；
// 日切时旧日 entries 进 pending 队列，下次 Flush 先写 pending 再写当前。
// 严禁增量合并 avg。
type comboAccumulator struct {
	day     time.Time // UTC 日期（截断到日）
	entries map[string]*comboAccumEntry
	pending []pendingDay // 日切时未 flush 的旧日 entries（样本不丢语义）
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

// pendingDay 日切时暂存的旧日 entries（确保最终落库）。
type pendingDay struct {
	day     time.Time
	entries map[string]*comboAccumEntry
}

// newComboAccumulator 创建累积器。
func newComboAccumulator() *comboAccumulator {
	return &comboAccumulator{
		entries: make(map[string]*comboAccumEntry),
	}
}

// Record 喂入一个样本（全组合含低于门槛）。
// 日切时旧日 entries 进 pending 队列（样本不丢语义），开新一日。
func (a *comboAccumulator) Record(comboKey, typ string, netBps float64, ts time.Time) {
	utcDay := ts.UTC().Truncate(24 * time.Hour)
	if !utcDay.Equal(a.day) {
		// 日切：旧日 entries 进 pending（如果非零日且有未 flush 数据）
		if !a.day.IsZero() && len(a.entries) > 0 {
			a.pending = append(a.pending, pendingDay{day: a.day, entries: a.entries})
		}
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
// 先写 pending 旧日 entries，再写当前日 entries；当前日不清零（累计覆盖语义）。
func (a *comboAccumulator) Flush(ctx context.Context, store *pgstore.Store) error {
	// 先写 pending 旧日数据（样本不丢语义）
	for _, pd := range a.pending {
		if err := flushDay(ctx, store, pd.day, pd.entries); err != nil {
			return err
		}
	}
	a.pending = a.pending[:0]
	// 再写当前日（不清零——累计覆盖：后写覆盖前写，Σ/count/min/max 单调完善）
	if len(a.entries) == 0 {
		return nil
	}
	return flushDay(ctx, store, a.day, a.entries)
}

// flushDay 将一组 entries 写入 DB（提取共用逻辑）。
func flushDay(ctx context.Context, store *pgstore.Store, day time.Time, entries map[string]*comboAccumEntry) error {
	if len(entries) == 0 {
		return nil
	}
	rows := make([]pgstore.ComboDailyRow, 0, len(entries))
	for _, e := range entries {
		netAvg := e.sum / float64(e.count)
		var annualAvg *float64
		if e.typ == "FUNDING_HEDGE" || e.typ == "PERP_FUNDING" || e.typ == "XASSET_CARRY" { // D-332 补型：netBps=年化百分点同口径
			v := netAvg
			annualAvg = &v
		}
		rows = append(rows, pgstore.ComboDailyRow{
			ComboKey:  e.comboKey,
			Day:       day,
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
