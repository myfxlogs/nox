// d111_fastmove_test.go —— D-111 锚测：①快行情挂起闸（位移超阈→不判+清 breach+首报 warn）；
// ②静市不误伤（闸开 + 无位移 → 灾难带照杀）；③平仓成功 warn alert 落库（feishu 扇出源）。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// d111Cfg 在 d378b 基准上开快行情闸（25bps/90s）。
func d111Cfg() Config {
	c := d378bCfg()
	c.XAssetFastMoveBps = 25
	c.XAssetFastMoveWindow = 90 * time.Second
	return c
}

// A1：#509 场景回放——MT 腿 2 秒内滞后跳价撕开假偏离：位移超阈 → 挂起，breach 计数清零
// 且不误杀；episode 首报 warn 落 alerts。
func TestD111_FastMove_SuspendNoKill(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	e.cfg = d111Cfg()
	qb := e.quotes
	now := time.Now()
	// 帧1：平静——basis 带内，播种位移窗
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: 100.0, Ask: 100.05, Time: now.Add(-60 * time.Second)})
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.05, Ask: 100.10, Time: now.Add(-60 * time.Second)})
	ctx := context.Background()
	p := d377OpenPos(t, st, posID)
	if should, _ := e.checkExitCondition(ctx, p); should {
		t.Fatal("帧1 平静场不应平仓")
	}
	// 帧2：MT mid 100.025→99.325（位移≈70bps>25）且 basis 破带（dev≈71>40）——闸必须拦下
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: 99.30, Ask: 99.35, Time: now})
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.05, Ask: 100.10, Time: now})
	for i := 0; i < 3; i++ { // 连续多轮破带仍应挂起（debounce 不累积）
		if should, reason := e.checkExitCondition(ctx, p); should {
			t.Fatalf("快行情中 dev 破带不得平仓, got close reason=%q", reason)
		}
	}
	if got := d378bEvalCount(t, e, posID); got != 0 {
		t.Fatalf("suspend 应清 breach 计数, got %d", got)
	}
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE position_id=$1 AND level='warn' AND message LIKE '%快行情%'`, posID).Scan(&n); err != nil {
		t.Fatalf("alert query: %v", err)
	}
	if n != 1 {
		t.Fatalf("episode 首报应恰 1 条 warn, got %d", n)
	}
}

// A2：闸开但静市（双腿零位移）——灾难带语义不变，连 2 轮破带照杀。
// A2：闸开 + 静市 + CEX 腿结构断裂——瞬时跳价窗内挂起（位移>阈），断裂持续超出
// 90s 窗后照杀（结构断裂 outlive 快行情窗，这正是 D-113 语义：真断裂杀、瞬时撕价不杀）。
func TestD111_FastMove_CalmStillKills(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	e.cfg = d111Cfg()
	qb := e.quotes
	now := time.Now()
	// 静市帧：basis 贴锚，共识源就位
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: 100.01, Ask: 100.06, Time: now})
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.05, Ask: 100.10, Time: now})
	d113PublishConsensus(t, e, 100.0)
	ctx := context.Background()
	p := d377OpenPos(t, st, posID)
	if should, _ := e.checkExitCondition(ctx, p); should { // 锚定轮
		t.Fatal("锚定轮不应平仓")
	}
	// CEX 腿断裂：mid 100.075→100.55（位移 48bps>25 → 窗内挂起）
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.50, Ask: 100.60, Time: now.Add(10 * time.Second)})
	if should, _ := e.checkExitCondition(ctx, p); should {
		t.Fatal("断裂瞬时仍在位移窗内 → 挂起不杀")
	}
	// 窗滑过（>90s）：断裂持续——恢复判定，debounce 2 轮后杀
	post := now.Add(95 * time.Second)
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: 100.01, Ask: 100.06, Time: post})
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.50, Ask: 100.60, Time: post})
	if should, _ := e.checkExitCondition(ctx, p); should {
		t.Fatal("窗后第 1 轮应仅计数（debounce=2）")
	}
	should, reason := e.checkExitCondition(ctx, p)
	if !should || !strings.HasPrefix(reason, "disaster: cex-leg dev") {
		t.Fatalf("断裂持续超窗应 disaster 平仓, got %v %q", should, reason)
	}
}

// A3：平仓成功通知——warn alert 落库含 reason（feishu 扇出源；realized NULL 时省略不阻塞）。
func TestD111_CloseNotify_AlertRow(t *testing.T) {
	st, posID, e := d378bSeedXAsset(t, -9.0, 6.9)
	e.cfg = d111Cfg()
	e.notifyPositionClosed(context.Background(), &pgstore.Position{ID: posID, Type: "XASSET_CARRY"}, "exit: disaster: dev 42.0 > band 40.0")
	var msg string
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT message FROM alerts WHERE position_id=$1 AND level='warn' ORDER BY id DESC LIMIT 1`, posID).Scan(&msg); err != nil {
		t.Fatalf("close notify 应有 warn 行: %v", err)
	}
	if !strings.Contains(msg, "自动平仓") || !strings.Contains(msg, "disaster") {
		t.Fatalf("通知文本应含触发源, got %q", msg)
	}
}
