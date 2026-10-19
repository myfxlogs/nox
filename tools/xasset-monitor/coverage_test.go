package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/data/marketstate"
	"nox/internal/store/pgstore"
)

// covAlerts 拉含指定 pair 片段的覆盖告警。
func covAlerts(t *testing.T, s *pgstore.Store, pair string) []string {
	t.Helper()
	rows, err := s.Pool().Query(context.Background(),
		`SELECT message FROM alerts WHERE message LIKE '数据覆盖%' AND message LIKE '%'||$1||'%'`, pair)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if rows.Scan(&m) != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// covInsertRow 插一条簿行（ts=now-ago）。
func covInsertRow(t *testing.T, s *pgstore.Store, pair string, ago time.Duration) {
	t.Helper()
	_, err := s.Pool().Exec(context.Background(),
		`INSERT INTO fin_xasset_monitor (pair, cex_venue, mt_venue, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, harvest_floor_bps, ts)
	VALUES ($1,'binance:perp','3001157:mt5',4355,4356,4354,4355,12,5,3,0.001,21.9,1.0,24.9,9,7,$2)`,
		pair, time.Now().Add(-ago))
	if err != nil {
		t.Fatal(err)
	}
}

// covSession 写第 idx 条周会话窗 fact（wday/startMin/endMin；多窗=多索引行
// #0..#k+#n 计数——LatestFactsByKind 按 (venue,symbol) 取最新，同键互覆）；
// start=end=1440=永闭窗。cnt=0 不写计数行（单窗夹具用）。
func covSession(t *testing.T, s *pgstore.Store, symbol string, idx, cnt, wday, startMin, endMin int) {
	t.Helper()
	ctx := context.Background()
	if idx == 0 {
		if _, err := s.Pool().Exec(ctx, `DELETE FROM facts WHERE kind=$1 AND venue='3001157' AND (symbol=$2 OR symbol LIKE $2||'#%')`,
			fact.KindTradeSession, symbol); err != nil {
			t.Fatal(err)
		}
	}
	val := float64(wday*1e8 + startMin*1e4 + endMin)
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ($1,'3001157',$2,$3,'min',now(),'covtest')`,
		fact.KindTradeSession, fmt.Sprintf("%s#%d", symbol, idx), val); err != nil {
		t.Fatal(err)
	}
	if cnt > 0 {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ($1,'3001157',$2,$3,'cnt',now(),'covtest')`,
			fact.KindTradeSession, symbol+"#n", cnt); err != nil {
			t.Fatal(err)
		}
	}
}

func covClean(t *testing.T, s *pgstore.Store) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM alerts WHERE message LIKE '数据覆盖%'`,
		`DELETE FROM fin_xasset_monitor WHERE pair LIKE 'COV%'`,
		`DELETE FROM facts WHERE kind='trade_session' AND venue='3001157' AND src='covtest'`,
	} {
		if _, err := s.Pool().Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCoverageAudit D-625 P1 两型锚：24h 行数<12→饥饿 warn；最新行龄>15min
// →断供 warn（各末行实施席令）。会话表缺席（测试库无该键会话事实）=无证据
// 不误赦→原阈直判，两型各一发。
func TestCoverageAudit(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()
	covClean(t, s)
	// 饥饿对：3 行陈旧（>15min）→两型各一发。
	for i := 0; i < 3; i++ {
		covInsertRow(t, s, "COVTEST", time.Duration(60+i)*time.Minute)
	}
	coverageAudit(ctx, s, []monitorPair{{cexVenue: "binance:perp", pair: "COVTEST", mtVenueKey: "3001157:mt5"}})
	var hungry, stale bool
	for _, m := range covAlerts(t, s, "COVTEST") {
		if strings.Contains(m, "饥饿") && strings.Contains(m, "[角色:实施]") {
			hungry = true
		}
		if strings.Contains(m, "断供") && strings.Contains(m, "[角色:实施]") {
			stale = true
		}
	}
	if !hungry || !stale {
		t.Fatalf("两型告警缺一: hungry=%v stale=%v", hungry, stale)
	}
	var _ = pgstore.XAssetCoverageRow{}
}

// TestCoverageAuditDeadKey 死键滤除锚（10-19 销账票）：簿行在表但不在本拍派生
// 清单（组合退役/demand 缩编退场）→零告警——对抗锚：删 live 过滤行此测必红。
func TestCoverageAuditDeadKey(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()
	covClean(t, s)
	covInsertRow(t, s, "COVDEAD", 2*time.Hour)
	coverageAudit(ctx, s, []monitorPair{{cexVenue: "binance:perp", pair: "COVOTHER", mtVenueKey: "3001157:mt5"}})
	if n := len(covAlerts(t, s, "COVDEAD")); n != 0 {
		t.Fatalf("死键仍被告警: %d 条", n)
	}
}

// TestCoverageAuditSessionWindow 会话闸窗口化锚（10-19 销账票）：
// ①停更窗全落闭市内（永闭窗）→两型豁免零告警——对抗锚：删 OpenSince 豁免此测必红；
// ②全天窗（24/7 全开）→窗内静默照报断供（闭市豁免不掩盖真断供）；
// ③饥饿阈开市折算：低产窗（开市 ~80min/24h）5 行 ≥ 折算阈（0）→不报饿，
//
//	同样 5 行在全天窗→原阈 12 照报——周末整停 Monday 晨误报形态根治。
func TestCoverageAuditSessionWindow(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()

	t.Run("永闭窗豁免断供", func(t *testing.T) {
		covClean(t, s)
		covSession(t, s, "COVCAP", 0, 0, 1, 1440, 1440) // 周一 1440-1440：永闭（nm≤1439 恒不中）
		covInsertRow(t, s, "COVCEX-COVCAP", 2*time.Hour)
		coverageAudit(ctx, s, []monitorPair{{cexVenue: "binance:perp", pair: "COVCEX-COVCAP", mtVenueKey: "3001157:mt5"}})
		if n := len(covAlerts(t, s, "COVCAP")); n != 0 {
			t.Fatalf("停更窗全闭市仍告警: %d 条: %v", n, covAlerts(t, s, "COVCAP"))
		}
	})

	t.Run("全天窗静默照报", func(t *testing.T) {
		covClean(t, s)
		for d := 0; d < 7; d++ {
			covSession(t, s, "COVCAP", d, 7, d, 0, 0) // 全天窗×7=24/7 全开
		}
		covInsertRow(t, s, "COVCEX-COVCAP", 2*time.Hour)
		coverageAudit(ctx, s, []monitorPair{{cexVenue: "binance:perp", pair: "COVCEX-COVCAP", mtVenueKey: "3001157:mt5"}})
		var stale bool
		for _, m := range covAlerts(t, s, "COVCAP") {
			if strings.Contains(m, "断供") {
				stale = true
			}
		}
		if !stale {
			t.Fatal("24/7 全开窗内静默未报断供（闭市豁免掩盖真断供）")
		}
	})

	t.Run("饥饿阈开市折算", func(t *testing.T) {
		now := time.Now()
		nm := now.Hour()*60 + now.Minute()
		// 低开窗：今晨 [nm-40, nm+40] 一窗（±边界夹 now 保测试执行期内窗活）；
		// nm 贴边时平移出 wrap 区。
		start, end := nm-40, nm+40
		if start < 1 {
			start, end = 1, 81
		}
		if end > 1439 {
			start, end = 1359, 1439
		}
		for _, tc := range []struct {
			name      string
			fixedOpen bool
			wantAlert bool
		}{
			{"低开窗5行不报饿", false, false}, // 阈=12*80/1440=0 → 5≥0 豁免
			{"全开窗5行照报饿", true, true},   // 全天窗×7 → 阈 12 → 5<12 报
		} {
			covClean(t, s)
			if tc.fixedOpen {
				for d := 0; d < 7; d++ {
					covSession(t, s, "COVCAP", d, 7, d, 0, 0)
				}
			} else {
				covSession(t, s, "COVCAP", 0, 0, int(now.Weekday()), start, end)
			}
			covInsertRow(t, s, "COVCEX-COVCAP", 5*time.Minute) // 新鲜→只涉饿型
			coverageAudit(ctx, s, []monitorPair{{cexVenue: "binance:perp", pair: "COVCEX-COVCAP", mtVenueKey: "3001157:mt5"}})
			got := len(covAlerts(t, s, "COVCAP"))
			if tc.wantAlert && got == 0 {
				t.Fatalf("%s: 期望报饿实际零告警", tc.name)
			}
			if !tc.wantAlert && got != 0 {
				t.Fatalf("%s: 期望豁免实际 %d 条: %v", tc.name, got, covAlerts(t, s, "COVCAP"))
			}
		}
	})
}

// TestOpenMinutesTable marketstate 窗口原语锚：OpenMinutes/OpenSince 计数与缺席语义。
func TestOpenMinutesTable(t *testing.T) {
	tbl := marketstate.Table{
		"3001157|X": {{Wday: 1, StartMin: 600, EndMin: 660}}, // 周一 10:00-11:00
	}
	now := time.Date(2026, 10, 19, 10, 30, 0, 0, time.UTC) // 周一 10:30 市中
	if got, known := tbl.OpenMinutes("3001157", "X", now.Add(-24*time.Hour), now); got != 31 || !known {
		t.Fatalf("OpenMinutes 24h=31 分预期（窗端含）, got=%d known=%v", got, known)
	}
	if open, known := tbl.OpenSince("3001157", "X", now.Add(-29*time.Minute), now); !open || !known {
		t.Fatalf("窗内 29 分钟有开市, OpenSince 应 true, got=%v known=%v", open, known)
	}
	if open, _ := tbl.OpenSince("3001157", "X", now.Add(2*time.Hour), now.Add(3*time.Hour)); open {
		t.Fatal("全闭窗 OpenSince 应 false")
	}
	if _, known := tbl.OpenMinutes("3001157", "MISSING", now, now.Add(time.Hour)); known {
		t.Fatal("表缺席应 known=false")
	}
}
