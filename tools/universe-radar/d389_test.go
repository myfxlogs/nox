// d389_test.go —— D-389 验收锚 A/D/E/F（digest 边沿化）。
package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeRadarStore 锚 D/E 注入面（radarStore 接口）。
type fakeRadarStore struct {
	digests   map[string]string
	upserts   map[string]string // 已落库的 kind→hash
	touches   []alertTouch
	upsertErr error
}

func (f *fakeRadarStore) FeishuChatID(context.Context) (string, error) { return "oc_test", nil }
func (f *fakeRadarStore) UpsertRadarWatchlist(context.Context, string, []string, string) error {
	return nil
}
func (f *fakeRadarStore) TouchCexUniverseAlert(_ context.Context, v, s string, _ time.Time) error {
	f.touches = append(f.touches, alertTouch{v, s})
	return nil
}
func (f *fakeRadarStore) GetRadarDigests(context.Context) (map[string]string, error) {
	m := map[string]string{}
	for k, v := range f.digests {
		m[k] = v
	}
	return m, nil
}
func (f *fakeRadarStore) UpsertRadarDigest(_ context.Context, kind, hash string) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserts[kind] = hash
	f.digests[kind] = hash
	return nil
}

func newFakeRadarStore() *fakeRadarStore {
	return &fakeRadarStore{digests: map[string]string{}, upserts: map[string]string{}}
}

// 锚 A：stored==当前集合 hash → 行不生成；集合改一成员 → 行复现。四 kind 同矩阵。
func TestD389_AnchorA_DigestEdgeTrigger(t *testing.T) {
	kinds := map[string][]string{
		"legless_band":   {"BTCUSDT", "ETHUSDT"},
		"class_filtered": {"XAUUSD", "NAS100"},
		"real_leg":       {"BTC binance×okx", "ETH binance×gate"},
		"suspect":        {"DOGEUSDT"},
	}
	for kind, keys := range kinds {
		stored := map[string]string{kind: digestHash(keys)}
		pending := map[string]string{}
		var lines []string
		pushDigestIfChanged(stored, pending, &lines, kind, keys, "digest-line")
		if len(lines) != 0 || len(pending) != 0 {
			t.Fatalf("%s: 集合不变应静默，got lines=%v pending=%v", kind, lines, pending)
		}
		// 集合变一成员 → 行复现 + pending 记新 hash
		changed := append(append([]string(nil), keys...), "NEWSYM")
		pushDigestIfChanged(stored, pending, &lines, kind, changed, "digest-line")
		if len(lines) != 1 || pending[kind] != digestHash(changed) {
			t.Fatalf("%s: 集合变更应入队，got lines=%v pending=%v", kind, lines, pending)
		}
	}
	// 成员顺序变化不算变更（sort 归一）
	stored := map[string]string{"suspect": digestHash([]string{"A", "B"})}
	var lines []string
	pending := map[string]string{}
	pushDigestIfChanged(stored, pending, &lines, "suspect", []string{"B", "A"}, "x")
	if len(lines) != 0 {
		t.Fatalf("顺序变化不应触发推送: %v", lines)
	}
}

// 锚 D（R2）：UpsertRadarDigest 失败=上抛 error（改前静默吞=红）；失败不落 hash，
// 下轮同集合仍判变重推；修复路径下轮成功落 hash 后第三轮静默。
func TestD389_AnchorD_UpsertFailurePropagates(t *testing.T) {
	fs := newFakeRadarStore()
	fs.upsertErr = errors.New("pg down")
	okSend := func(context.Context, []string) error { return nil }
	pending := map[string]string{"suspect": digestHash([]string{"A"})}

	// 本轮：行已推（send 成功）但 upsert 失败 → 必须上抛，hash 不落库。
	if err := pushAndBookkeep(context.Background(), fs, okSend, []string{"l"}, pending, nil, time.Now()); err == nil {
		t.Fatal("UpsertRadarDigest 失败须上抛 error（静默吞=永久重推）")
	}
	if len(fs.upserts) != 0 {
		t.Fatalf("失败不应落 hash: %v", fs.upserts)
	}
	// 下轮：GetRadarDigests 未命中 → 同集合仍判变重推（重试语义正确）。
	var lines []string
	p2 := map[string]string{}
	pushDigestIfChanged(fs.digests, p2, &lines, "suspect", []string{"A"}, "l")
	if len(lines) != 1 {
		t.Fatal("hash 未落下轮应重推")
	}
	// upsert 恢复后落库，第三轮同集合静默。
	fs.upsertErr = nil
	if err := pushAndBookkeep(context.Background(), fs, okSend, lines, p2, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	var lines3 []string
	p3 := map[string]string{}
	pushDigestIfChanged(fs.digests, p3, &lines3, "suspect", []string{"A"}, "l")
	if len(lines3) != 0 {
		t.Fatal("hash 已落后同集合应静默")
	}
}

// 锚 E（R3）：SendText 注入 error → 整批丢弃但 pendingKinds 零写入 +
// 边沿事件冷却戳零写入（下轮可重发）——批失败不吞真信号冷却。
func TestD389_AnchorE_SendFailureNoBookkeeping(t *testing.T) {
	fs := newFakeRadarStore()
	failSend := func(context.Context, []string) error { return errors.New("feishu 500") }
	pending := map[string]string{"class_filtered": "h1"}
	touches := []alertTouch{{"binance", "BTCUSDT"}}

	err := pushAndBookkeep(context.Background(), fs, failSend, []string{"edge", "digest"}, pending, touches, time.Now())
	if err != nil {
		t.Fatalf("发送失败不应上抛（整批丢、下轮重试）: %v", err)
	}
	if len(fs.upserts) != 0 || len(fs.digests) != 0 {
		t.Fatalf("发送失败 digest hash 不应落库: upserts=%v digests=%v", fs.upserts, fs.digests)
	}
	if len(fs.touches) != 0 {
		t.Fatalf("发送失败冷却戳不应盖（信号须可重发）: %v", fs.touches)
	}
}

// 锚 F（R1）：real_leg 集合不变、仅费率漂移 ±30% → hash 不变不推（选项 a 机检）。
func TestD389_AnchorF_RateDriftNoRepublish(t *testing.T) {
	p1 := divPair{Canon: "BTC", ShortVenue: "binance", LongVenue: "okx", FundShort: 10.0, FundLong: -5.0}
	p2 := p1
	p2.FundShort, p2.FundLong = 13.0, -3.5 // ±30% 漂移
	if realLegKey(p1) != realLegKey(p2) {
		t.Fatal("pair 身份键不应含费率")
	}
	stored := map[string]string{"real_leg": digestHash([]string{realLegKey(p1)})}
	var lines []string
	pending := map[string]string{}
	pushDigestIfChanged(stored, pending, &lines, "real_leg", []string{realLegKey(p2)}, "drifted")
	if len(lines) != 0 {
		t.Fatal("费率漂移 pair 身份不变应不推（R0-A1 选项 a 取舍）")
	}
	// pair 身份真变（venue 换）→ 推
	p3 := divPair{Canon: "BTC", ShortVenue: "gate", LongVenue: "okx"}
	pushDigestIfChanged(stored, pending, &lines, "real_leg", []string{realLegKey(p1), realLegKey(p3)}, "newpair")
	if len(lines) != 1 {
		t.Fatal("pair 集合真变更应推送")
	}
}

// 发送成功后记账：hash 落库 + 冷却戳盖（正常路径对照组）。
func TestD389_PushSuccessBookkeeping(t *testing.T) {
	fs := newFakeRadarStore()
	okSend := func(context.Context, []string) error { return nil }
	pending := map[string]string{"legless_band": "hx"}
	touches := []alertTouch{{"okx", "ETHUSDT"}}
	if err := pushAndBookkeep(context.Background(), fs, okSend, []string{"l"}, pending, touches, time.Now()); err != nil {
		t.Fatal(err)
	}
	if fs.digests["legless_band"] != "hx" || len(fs.touches) != 1 {
		t.Fatalf("成功后应落 hash+冷却戳: %v %v", fs.digests, fs.touches)
	}
}
