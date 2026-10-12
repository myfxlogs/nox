// selfwatch.go —— D-033 系统自盯汇报面：paper 观测窗 cohort/Stage 闸/数据面异常
// 所需的窄读口（全 DB 单源，调度器只在唤醒时刻查一次——数据不缓存）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"nox/internal/data/keydomain"
	"nox/internal/platform/alerttext"
)

// PaperWindowCohort paper_window_report.sh cohort 口径进程内化：
// structure 行中（entry_ts>=since ∪ status='active'）∪ 窗内退出行（exit_ts>=since）。
// 超集直取，报告侧按 status/时戳精确分桶。
func (s *Store) PaperWindowCohort(ctx context.Context, structure string, since time.Time) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry
		WHERE structure = $1 AND (entry_ts >= $2 OR status = 'active' OR exit_ts >= $2)
		ORDER BY id`, structure, since)
}

// PaperActiveSplit D-050 ②：纸面在仓两轨分列观测（行数+品种数；无阈值不告警）。
// CEX 轨=long_spot_short_perp；CFD 轨=cfd×perp 双结构（mt×cex）；other=mtm 等兜底列。
type PaperActiveSplit struct {
	CexRows, CexSyms     int
	CfdRows, CfdSyms     int
	OtherRows, OtherSyms int
}

// PaperActiveStats 纸面在仓两轨分列单查询。
func (s *Store) PaperActiveStats(ctx context.Context) (PaperActiveSplit, error) {
	var sp PaperActiveSplit
	err := s.pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE structure = 'long_spot_short_perp'),
		  count(DISTINCT symbol) FILTER (WHERE structure = 'long_spot_short_perp'),
		  count(*) FILTER (WHERE structure IN ('long_cfd_short_perp','short_cfd_long_perp')),
		  count(DISTINCT symbol) FILTER (WHERE structure IN ('long_cfd_short_perp','short_cfd_long_perp')),
		  count(*) FILTER (WHERE structure NOT IN ('long_spot_short_perp','long_cfd_short_perp','short_cfd_long_perp')),
		  count(DISTINCT symbol) FILTER (WHERE structure NOT IN ('long_spot_short_perp','long_cfd_short_perp','short_cfd_long_perp'))
		FROM paper_spot_carry WHERE status = 'active'`,
	).Scan(&sp.CexRows, &sp.CexSyms, &sp.CfdRows, &sp.CfdSyms, &sp.OtherRows, &sp.OtherSyms)
	if err != nil {
		return PaperActiveSplit{}, fmt.Errorf("paper active stats: %w", err)
	}
	return sp, nil
}

// AppConfigString 单键字符串直读（行缺 → ok=false；D-033 窗起点/macro 闸等热读键）。
func (s *Store) AppConfigString(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_config WHERE key = $1`, key).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("app config string %s: %w", key, err)
	}
	return v, true, nil
}

// SchemaMigrationMax 已应用迁移最高版本（D-033 Stage 闸「D-032 在线」行——
// 迁移表=部署事实单源）。
func (s *Store) SchemaMigrationMax(ctx context.Context) (int, error) {
	var v int
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("schema migration max: %w", err)
	}
	return v, nil
}

// ExposureCapCounts 已启用敞口闸行数（venue 级/canonical 级）——Stage 闸「cap 已定」。
func (s *Store) ExposureCapCounts(ctx context.Context) (venueCaps, canonicalCaps int, err error) {
	if err = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM fin_venue_exposure_caps WHERE enabled`).Scan(&venueCaps); err != nil {
		return 0, 0, fmt.Errorf("venue caps count: %w", err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM fin_canonical_exposure_caps WHERE enabled`).Scan(&canonicalCaps); err != nil {
		return 0, 0, fmt.Errorf("canonical caps count: %w", err)
	}
	return venueCaps, canonicalCaps, nil
}

// XAssetRealCombo 实盘启用组合的精确配对键（D-056①：venue 对→四元组）——
// VenueA/VenueB 与 paper_spot_carry.venue_a/venue_b 同口径（cfd 行 A=MT 臂 B=CEX 臂；
// 双 CEX 行 A=spot B=perp）；Canonical=配对品种原值（MT 腿/同 canonical 腿直出，
// 规范化由消费方 NormalizeJoinKey 同口径）；Structure 由 venue_key 类型段推导；
// 构型不可判定（双腿 MT / 无 spot-perp 对）→ Structure 空=Stage 检查按无品种 fail-closed。
type XAssetRealCombo struct {
	VenueA    string
	VenueB    string
	Canonical string
	Structure string
}

// XAssetAllowRealPairs 实盘启用组合精确键（Stage 1 组合证据的组合域）。
func (s *Store) XAssetAllowRealPairs(ctx context.Context) ([]XAssetRealCombo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue_key_buy, venue_key_sell, canonical_buy, canonical_sell FROM xasset_allow_combos
		WHERE enabled AND COALESCE(simulated, false) = false
		ORDER BY venue_key_buy, venue_key_sell`)
	if err != nil {
		return nil, fmt.Errorf("xasset allow real pairs: %w", err)
	}
	defer rows.Close()
	var out []XAssetRealCombo
	for rows.Next() {
		var bk, sk, cb, cs string
		if err := rows.Scan(&bk, &sk, &cb, &cs); err != nil {
			return nil, fmt.Errorf("xasset allow real pairs scan: %w", err)
		}
		out = append(out, xassetRealCombo(bk, sk, cb, cs))
	}
	return out, rows.Err()
}

// xassetRealCombo 白名单行→paper 配对键推导（与 paper_all_in.cfdComboLegs 同规则）：
// 恰一 MT 腿 → cfd 双结构（MT 买腿=多 CFD 空 perp，MT 卖腿=空 CFD 多 perp）；
// 双 CEX 恰一 spot+恰一 perp → long_spot_short_perp（spot 买腿）；其余构型不可判
// → Structure 空串（Stage 检查 fail-closed 标缺）。
func xassetRealCombo(bk, sk, cb, cs string) XAssetRealCombo {
	buyMT, sellMT := keydomain.IsMTKey(bk), keydomain.IsMTKey(sk)
	buyT, sellT := keydomain.Parse(bk).Type, keydomain.Parse(sk).Type
	switch {
	case buyMT && !sellMT:
		return XAssetRealCombo{VenueA: bareVenueOf(bk), VenueB: bareVenueOf(sk), Canonical: cb, Structure: "long_cfd_short_perp"}
	case sellMT && !buyMT:
		return XAssetRealCombo{VenueA: bareVenueOf(sk), VenueB: bareVenueOf(bk), Canonical: cs, Structure: "short_cfd_long_perp"}
	case buyT == "spot" && sellT == "perp":
		return XAssetRealCombo{VenueA: bareVenueOf(bk), VenueB: bareVenueOf(sk), Canonical: cb, Structure: "long_spot_short_perp"}
	case sellT == "spot" && buyT == "perp":
		return XAssetRealCombo{VenueA: bareVenueOf(sk), VenueB: bareVenueOf(bk), Canonical: cs, Structure: "short_spot_long_perp"}
	}
	return XAssetRealCombo{VenueA: bareVenueOf(bk), VenueB: bareVenueOf(sk)}
}

// XAssetAllowRealCount xasset_allow_combos 实盘域启用行数（Stage 闸「白名单已定」；
// simulated=true=仅 demo 域行不计——D-030 Stage 1 范围是真实组合）。
func (s *Store) XAssetAllowRealCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM xasset_allow_combos
		WHERE enabled AND COALESCE(simulated, false) = false`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("xasset allow real count: %w", err)
	}
	return n, nil
}

// LatestFactTs 指定 kind 最新事实 ts（facts 新鲜度哨兵；零行 → ok=false）。
func (s *Store) LatestFactTs(ctx context.Context, kind string) (time.Time, bool, error) {
	var ts *time.Time
	err := s.pool.QueryRow(ctx, `SELECT MAX(ts) FROM facts WHERE kind = $1`, kind).Scan(&ts)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("latest fact ts %s: %w", kind, err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

// CountFactsSince 指定 kind 窗内事实行数（trade_session 缺席判定用）。
func (s *Store) CountFactsSince(ctx context.Context, kind string, since time.Time) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM facts WHERE kind = $1 AND ts >= $2`, kind, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("count facts %s: %w", kind, err)
	}
	return n, nil
}

// MacroEventMaxCreated 财历表最新插入时点（macro_event.created_at=落库时刻——
// UpsertMacroEvents 冲突不动 created_at，新事件进 14d 前窗才推进；
// 拉取停滞判读阈值在调用方）。
func (s *Store) MacroEventMaxCreated(ctx context.Context) (time.Time, bool, error) {
	var ts *time.Time
	err := s.pool.QueryRow(ctx, `SELECT MAX(created_at) FROM macro_event`).Scan(&ts)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("macro max created: %w", err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

// MakerLegStatsSince 自 since 起 maker 域进度（Stage 2「maker 分级」行）：
// opens=含 ≥1 条 fill_kind='maker' FILLED 腿的仓数；incidents=其中事故仓数
// （incidentGatePredicate 口径，与事故闸同源）。
func (s *Store) MakerLegStatsSince(ctx context.Context, since time.Time) (opens, incidents int, err error) {
	err = s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(DISTINCT p.id),
		       COUNT(DISTINCT p.id) FILTER (WHERE %s)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE p.opened_at >= $1
		  AND EXISTS (SELECT 1 FROM position_legs l
		              WHERE l.position_id = p.id AND l.fill_kind = 'maker' AND l.status = 'FILLED')`,
		incidentGatePredicate), since).Scan(&opens, &incidents)
	if err != nil {
		return 0, 0, fmt.Errorf("maker leg stats: %w", err)
	}
	return opens, incidents, nil
}

// InsertAlertDedup 指定窗口同键去重后写入（D-033 数据面告警 1h 去重——
// 不扰全局 30min 缺省窗；dedup 查询出错 fail-open 放行，同 insertAlert 先例）。
func (s *Store) InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error {
	message = alerttext.VenueErrCN(message)
	key := alerttext.AlertDedupKey(level, message)
	dup, err := s.recentAlertDuplicate(ctx, key, level, time.Now().Add(-window))
	if err != nil {
		log.Printf("[pgstore] alert dedup(%s) query fail (fail-open): %v", window, err)
	} else if dup {
		return nil
	}
	return s.insertAlert(ctx, nil, nil, level, message)
}

// MinWeekdaySessionStartMin D-061 ②：facts trade_session 周表某 weekday 的最早开盘分钟
// （value=day*1e8+startMin*1e4+endMin，UTC 坐标；day: 0=周日…1=周一…）。全表多 venue/
// symbol 投影取 min——MT 周开盘=该 weekday 最早会话起点。表缺/无该日行 → ok=false。
func (s *Store) MinWeekdaySessionStartMin(ctx context.Context, kind string, weekday int) (int, bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (venue, symbol) value, ts
		FROM facts WHERE kind = $1 ORDER BY venue, symbol, ts DESC`, kind)
	if err != nil {
		return 0, false, fmt.Errorf("weekday session start: %w", err)
	}
	defer rows.Close()
	minStart, ok := 0, false
	for rows.Next() {
		var v float64
		var ts time.Time
		if err := rows.Scan(&v, &ts); err != nil {
			return 0, false, fmt.Errorf("scan session fact: %w", err)
		}
		iv := int64(v)
		day, startMin := int(iv/1e8), int(iv/1e4)%1e4
		if day == weekday && (!ok || startMin < minStart) {
			minStart, ok = startMin, true
		}
	}
	return minStart, ok, rows.Err()
}
