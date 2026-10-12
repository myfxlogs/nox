// alert episode 态机测试锚。
package pgstore

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/platform/alerttext"
)

// seedEpisodes 直接落 app_config JSON（跨 now 边界场景的种子手段）。
func seedEpisodes(t *testing.T, s *Store, doc *alertEpisodeDoc) {
	t.Helper()
	buf, _ := json.Marshal(doc)
	if err := s.UpsertAppConfig(context.Background(), cfgAlertEpisodes, string(buf), "test"); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestD168_GateOpenPushSuppress(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	// 无集 → open+推。
	act, key, err := s.AlertEpisodeGate(ctx, "warn", "venue=x age=1m0s", now)
	if err != nil || act != EpisodePush {
		t.Fatalf("first: act=%v err=%v", act, err)
	}
	if key == "" {
		t.Fatal("key empty")
	}
	// 同键（分钟数不同归一键同）→ 抑制。
	act, _, _ = s.AlertEpisodeGate(ctx, "warn", "venue=x age=2m0s", now.Add(time.Minute))
	if act != EpisodeSuppress {
		t.Fatalf("dup: act=%v, want suppress", act)
	}
	// push_msg_id 回写。
	if err := s.RecordAlertEpisodePushMsg(ctx, key, "om_9"); err != nil {
		t.Fatalf("record: %v", err)
	}
	doc, _ := s.loadEpisodes(ctx)
	if doc.Episodes[key].PushMsgID != "om_9" {
		t.Errorf("push_msg_id=%q", doc.Episodes[key].PushMsgID)
	}
}

func TestD168_GateCloseReopen(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	key := alerttext.AlertDedupKey("warn", "venue=x stale")
	seedEpisodes(t, s, &alertEpisodeDoc{Episodes: map[string]*alertEpisode{
		key: {State: episodeOpen, FirstFired: now.Add(-48 * time.Hour),
			LastFired: now.Add(-25 * time.Hour), LastPush: now.Add(-48 * time.Hour)},
	}})
	// 键 >24h 无新行 → 旧集判闭 → 新集推。
	act, _, err := s.AlertEpisodeGate(ctx, "warn", "venue=x stale", now)
	if err != nil || act != EpisodePush {
		t.Fatalf("act=%v err=%v, want push", act, err)
	}
	doc, _ := s.loadEpisodes(ctx)
	if !doc.Episodes[key].FirstFired.After(now.Add(-time.Minute)) {
		t.Error("新集 first_fired 未重置")
	}
}

func TestD168_GateRemindOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	key := alerttext.AlertDedupKey("warn", "venue=y stall")
	// open 未 ack：last_fired 鲜（1h 前有新行），last_push 已 >24h。
	seedEpisodes(t, s, &alertEpisodeDoc{Episodes: map[string]*alertEpisode{
		key: {State: episodeOpen, FirstFired: now.Add(-30 * time.Hour),
			LastFired: now.Add(-time.Hour), LastPush: now.Add(-30 * time.Hour)},
	}})
	act, _, _ := s.AlertEpisodeGate(ctx, "warn", "venue=y stall", now)
	if act != EpisodeRemind {
		t.Fatalf("act=%v, want remind", act)
	}
	// 再发行 → 已提醒过，抑制。
	act, _, _ = s.AlertEpisodeGate(ctx, "warn", "venue=y stall", now.Add(time.Minute))
	if act != EpisodeSuppress {
		t.Fatalf("second: act=%v, want suppress（remind 单次）", act)
	}
}

func TestD168_AckPaths(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	k1 := alerttext.AlertDedupKey("warn", "venue=a age=1m")
	k2 := alerttext.AlertDedupKey("warn", "venue=b down")
	seedEpisodes(t, s, &alertEpisodeDoc{Episodes: map[string]*alertEpisode{
		k1: {State: episodeOpen, FirstFired: now, LastFired: now, LastPush: now, PushMsgID: "om_k1"},
		k2: {State: episodeOpen, FirstFired: now, LastFired: now, LastPush: now, PushMsgID: "om_k2"},
	}})
	// 引用回复 ack 单集。
	key, ok, err := s.AckAlertEpisodeByPushMsg(ctx, "om_k1")
	if err != nil || !ok || key != k1 {
		t.Fatalf("byMsg: key=%q ok=%v err=%v", key, ok, err)
	}
	doc, _ := s.loadEpisodes(ctx)
	if doc.Episodes[k1].State != episodeAcked || doc.Episodes[k2].State != episodeOpen {
		t.Fatal("单集 ack 状态错")
	}
	// acked 集续发行 → 抑制。
	if act, _, _ := s.AlertEpisodeGate(ctx, "warn", "venue=a age=5m", now.Add(time.Minute)); act != EpisodeSuppress {
		t.Errorf("acked ep act=%v, want suppress", act)
	}
	// 裸词 → 剩余全 ack。
	n, _ := s.AckAllAlertEpisodes(ctx)
	if n != 1 {
		t.Errorf("AckAll n=%d, want 1", n)
	}
}

// 面板 AckAlert 反查 + AckUnackedAlertsByKey 同步。
func TestD168_PanelAckSync(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	// 落两条同键 alert（分钟不同归一键同）+ 一条异键。
	if err := s.InsertAlert(ctx, nil, "warn", "venue=a age=1m"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// 单条 alert 验证反查即可（dedup 窗内同键本就插不进第二条）。
	key := alerttext.AlertDedupKey("warn", "venue=a age=1m")
	seedEpisodes(t, s, &alertEpisodeDoc{Episodes: map[string]*alertEpisode{
		key: {State: episodeOpen, FirstFired: now, LastFired: now, LastPush: now},
	}})
	var id int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM alerts WHERE level='warn' ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("q id: %v", err)
	}
	if err := s.AckAlertEpisodeByAlertID(ctx, id); err != nil {
		t.Fatalf("byAlertID: %v", err)
	}
	doc, _ := s.loadEpisodes(ctx)
	if doc.Episodes[key].State != episodeAcked {
		t.Error("面板 ack 未同步 episode")
	}
	// AckUnackedAlertsByKey 同步 alerts.acked。
	n, err := s.AckUnackedAlertsByKey(ctx, key)
	if err != nil || n != 1 {
		t.Fatalf("byKey n=%d err=%v, want 1", n, err)
	}
	var acked bool
	s.pool.QueryRow(ctx, `SELECT acked FROM alerts WHERE id=$1`, id).Scan(&acked)
	if !acked {
		t.Error("alerts.acked 未同步")
	}
}

// 扇出并发锚：同键 N 并发 gate → 恰一 Push（episodeMu 序列化）。
func TestD168_GateConcurrentOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	var pushes int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			act, _, err := s.AlertEpisodeGate(ctx, "warn", "venue=c flapping", now)
			if err == nil && act == EpisodePush {
				atomic.AddInt64(&pushes, 1)
			}
		}(i)
	}
	wg.Wait()
	if pushes != 1 {
		t.Fatalf("pushes=%d, want exactly 1", pushes)
	}
}
