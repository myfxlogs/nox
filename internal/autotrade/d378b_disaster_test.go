// d378b_disaster_test.go —— D-378b S3：灾难带锚 A1-A7（先红后绿）。A8=既有 SWAPARB/CARRY
// basisStopCheck 套件回归（代码逐字节不动，全量测试承载）。
// 锚值换算：basis=(buyBid−100.10)/100.05×1e4（sell ask 固定 100.10；ref=buyAsk 100.05）。
package autotrade

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/combo"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"
)

// d378bCfg 灾难带测试标准 cfg（494 型：band=max(40,4×noise)；debounce 2；新鲜度 5min）。
func d378bCfg() Config {
	return Config{
		BasisStopBps: 6.9, XAssetExecDriftBps: 8,
		XAssetDisasterBps: 40, XAssetDisasterNoiseMult: 4,
		XAssetDisasterDebounce: 2, XAssetQuoteMaxAge: 5 * time.Minute,
	}
}

// d378bSeedXAsset 插入 XASSET_CARRY 仓（anchor/noise 可配）+ 双腿 fill 使 fillBps=anchor
// （exec_drift 零漂移不截胡）。legs buy 100/sell=100×(1−anchor/1e4)。
func d378bSeedXAsset(t *testing.T, anchor, noise float64) (*pgstore.Store, int64, *Engine) {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE facts, positions, position_legs, position_evals CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('binance','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING; INSERT INTO broker_symbol_map (venue, broker_symbol, canonical) VALUES ('binance','PAXGUSDT','PAXGUSDT') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed symbol map: %v", err)
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps, expected_entry_spread_bps, entry_noise_bps)
		 VALUES ('XASSET_CARRY:XAUUSD:a:b','XASSET_CARRY','OPEN',0,$1,$2) RETURNING id`, anchor, noise).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	sellPrice := 100.0 * (1 - anchor/1e4)
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, price_actual, status)
		 VALUES ($1,1,'OPEN','277842155','mt5','XAUUSD','BUY',1,100,100.0,'FILLED'),
		        ($1,2,'OPEN','binance','perp','PAXGUSDT','SELL',1,100,$2,'FILLED')`, posID, sellPrice); err != nil {
		t.Fatalf("seed legs: %v", err)
	}
	return st, posID, &Engine{store: st, quotes: bus.New(), cfg: d378bCfg()}
}

// d378bPublishQuotes 发布双腿报价（buyAsk 固定 100.05=ref；sellAsk 固定 100.10；
// buyBid 由目标 basis 反推；quoteAge 可注入单腿过期）。
func d378bPublishQuotes(t *testing.T, e *Engine, targetBasis float64, sellAge time.Duration) {
	t.Helper()
	now := time.Now()
	buyBid := 100.10 + targetBasis/1e4*100.05
	qb := e.quotes
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: buyBid, Ask: 100.05, Time: now})
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.05, Ask: 100.10, Time: now.Add(-sellAge)})
}

// d378bEvalCount 读灾难去抖计数（白盒断言计数器行为）。
func d378bEvalCount(t *testing.T, e *Engine, posID int64) int {
	t.Helper()
	e.disasterMu.Lock()
	defer e.disasterMu.Unlock()
	return e.disasterBreach[posID]
}

// A1（494 回放）：quote 锚 −8.97、basis −15.9 尖峰（偏离 6.93 < band 40）→ 新链全程 hold
// （旧链在偏离 6.9bps 处已杀）。
func TestD378b_A1_494Replay_Hold(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -8.97, 6.69)
	d378bPublishQuotes(t, e, -15.9, 0)
	p := d377OpenPos(t, st, posID)
	should, reason := e.checkExitCondition(context.Background(), p)
	if should {
		t.Fatalf("A1: 494 回放应全程 hold（偏离 %.2f < band 40）, got %q", math.Abs(-15.9-(-8.97)), reason)
	}
	rows := d377Evals(t, st, posID)
	// D-406：hold 语义不变（未平仓）；五闸全过后轮转闸留证 → decision=hold_rotate_check。
	if len(rows) != 1 || rows[0].Decision != "hold_rotate_check" {
		t.Fatalf("A1: want 1 hold_rotate_check row, got %+v", rows)
	}
}

// A2：持续偏离 >40bps 连续 2 评估 → 第 2 次平仓 + decision=disaster 行 + exit_kind=disaster。
func TestD378b_A2_SustainedBreach_Disaster(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	d378bPublishQuotes(t, e, -59.0, 0) // dev=50 > band 40
	ctx := context.Background()
	p := d377OpenPos(t, st, posID)
	should1, _ := e.checkExitCondition(ctx, p)
	got1 := d378bEvalCount(t, e, posID)
	if should1 || got1 != 1 {
		t.Fatalf("A2: 第 1 次应 hold 且计数 1, got close=%v count=%d", should1, got1)
	}
	should2, reason2 := e.checkExitCondition(ctx, p)
	if !should2 || !strings.HasPrefix(reason2, "disaster: dev") {
		t.Fatalf("A2: 第 2 次应平仓 reason=disaster:…, got %v/%q", should2, reason2)
	}
	rows := d377Evals(t, st, posID)
	if len(rows) != 2 || rows[1].Decision != "disaster" {
		t.Fatalf("A2: want 第 2 行 decision=disaster, got %+v", rows)
	}
	if got := d378bEvalCount(t, e, posID); got != 0 {
		t.Fatalf("A2: 平仓后计数器应清零, got %d", got)
	}
	// exit_kind 映射（logCloseTrigger 同写法）
	if err := st.UpdatePositionExitKind(ctx, posID, pgstore.ExitKindFromReason(reason2)); err != nil {
		t.Fatalf("write kind: %v", err)
	}
	var kind string
	if err := st.Pool().QueryRow(ctx, `SELECT exit_kind FROM positions WHERE id=$1`, posID).Scan(&kind); err != nil {
		t.Fatalf("read kind: %v", err)
	}
	if kind != "disaster" {
		t.Fatalf("A2: exit_kind = %q, want disaster（reason 串不得含 basis/carry/funding/stop 子串）", kind)
	}
}

// A3：单次尖刺 >40bps 即回落 → 不平仓且计数器清零（连续性断言：尖刺-常态-尖刺 第三次仍 hold）。
func TestD378b_A3_SpikeResets(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	ctx := context.Background()
	p := d377OpenPos(t, st, posID)
	d378bPublishQuotes(t, e, -59.0, 0) // 尖刺 dev=50 → count=1
	if should, _ := e.checkExitCondition(ctx, p); should {
		t.Fatal("A3: 首次尖刺不应平仓")
	}
	d378bPublishQuotes(t, e, -14.0, 0) // 回常态 dev=5 → 计数清零
	if should, _ := e.checkExitCondition(ctx, p); should {
		t.Fatal("A3: 常态不应平仓")
	}
	d378bPublishQuotes(t, e, -59.0, 0) // 再尖刺：若未清零 count=2 会平（错）；清零后 count=1 → hold
	if should, _ := e.checkExitCondition(ctx, p); should {
		t.Fatal("A3: 尖刺-常态-尖刺 应仍 hold（计数器已清零，仅 count=1）")
	}
}

// A4：一腿 quote 超龄（10min > 5min）+ 偏离 50 → decision=fail_closed:stale、不平仓不计数。
func TestD378b_A4_Stale_FailClosed(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	d378bPublishQuotes(t, e, -59.0, 10*time.Minute) // sell 腿 stale
	p := d377OpenPos(t, st, posID)
	should, _ := e.checkExitCondition(context.Background(), p)
	if should {
		t.Fatal("A4: stale 不应平仓")
	}
	rows := d377Evals(t, st, posID)
	if len(rows) != 1 || rows[0].Decision != "fail_closed:stale" {
		t.Fatalf("A4: want fail_closed:stale, got %+v", rows)
	}
	if got := d378bEvalCount(t, e, posID); got != 0 {
		t.Fatalf("A4: stale 不计数, got %d", got)
	}
}

// A5：entry_noise=15 → 判距=max(40,4×15)=60——dev 50 不触（noise 外化生效）。
func TestD378b_A5_NoiseExternalized(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 15.0)
	d378bPublishQuotes(t, e, -59.0, 0) // dev=50 < band 60
	p := d377OpenPos(t, st, posID)
	should, _ := e.checkExitCondition(context.Background(), p)
	if should {
		t.Fatal("A5: dev 50 < band 60（noise 外化）不应平仓")
	}
	if got := d378bEvalCount(t, e, posID); got != 0 {
		t.Fatalf("A5: 未触带不应计数, got %d", got)
	}
}

// A6（495 回放）：anchor −5.07、basis 峰值 −20.3（偏离 15.23）→ 全程 hold（噪声极值不触灾难带）。
func TestD378b_A6_495Replay_Hold(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -5.07, 6.91)
	d378bPublishQuotes(t, e, -20.3, 0)
	p := d377OpenPos(t, st, posID)
	should, _ := e.checkExitCondition(context.Background(), p)
	if should {
		t.Fatal("A6: 495 回放（偏离 15.23 < 40）应全程 hold")
	}
	rows := d377Evals(t, st, posID)
	// D-406：hold 语义不变（未平仓）；五闸全过后轮转闸留证 → decision=hold_rotate_check。
	if len(rows) != 1 || rows[0].Decision != "hold_rotate_check" {
		t.Fatalf("A6: want hold_rotate_check row, got %+v", rows)
	}
}

// A7（C1 集成）：同组合 24h 内 3 次 disaster 平仓 → comboLossCheck 命中（kind 集含 disaster）。
func TestD378b_A7_C1DisasterCounted(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ck := combo.Key("SWAPARB", "AUDJPY", "a:mt5", "b:mt5")
	for i := 1; i <= 3; i++ {
		var id int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ($1,'SWAPARB','CLOSED',0) RETURNING id`,
			fmt.Sprintf("%s:%d", ck, i)).Scan(&id); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := st.Pool().Exec(ctx,
			`UPDATE positions SET exit_kind='disaster', closed_at=now()-make_interval(hours=>$2) WHERE id=$1`, id, i); err != nil {
			t.Fatalf("seed kind: %v", err)
		}
	}
	g := NewGate(st, Config{ComboLossLimit: 3, ComboLossWindow: 24 * time.Hour}, nil)
	hit, count, err := g.comboLossCheck(ctx, ck)
	if err != nil || !hit || count != 3 {
		t.Fatalf("A7: hit=%v count=%d err=%v, want true/3（kind 集含 disaster）", hit, count, err)
	}
}
