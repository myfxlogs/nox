// d392_test.go —— D-392 验收锚 A-H（digest 成员级边沿化：新入才推+flap 24h TTL）。
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// fakeRadarStore 成员级注入面（radarStore 接口）。
type fakeRadarStore struct {
	watchUpserts []string // D-532 S2：watchlist 写侧捕获（symbol|venues|reason）
	members      map[string]map[string]pgstore.RadarDigestMember
	touches      []alertTouch
	applyErr     error
	applied      bool // ApplyRadarDigestRun 是否被调
	sentLines    []string
}

func newFakeRadarStore() *fakeRadarStore {
	return &fakeRadarStore{members: map[string]map[string]pgstore.RadarDigestMember{}}
}

func (f *fakeRadarStore) FeishuChatID(context.Context) (string, error) { return "oc_test", nil }
func (f *fakeRadarStore) UpsertRadarWatchlist(_ context.Context, sym string, venues []string, reason string) error {
	f.watchUpserts = append(f.watchUpserts, sym+"|"+strings.Join(venues, ",")+"|"+reason)
	return nil
}
func (f *fakeRadarStore) TouchCexUniverseAlert(_ context.Context, v, s string, _ time.Time) error {
	f.touches = append(f.touches, alertTouch{v, s})
	return nil
}
func (f *fakeRadarStore) GetRadarDigestMembers(context.Context) (map[string]map[string]pgstore.RadarDigestMember, error) {
	out := map[string]map[string]pgstore.RadarDigestMember{}
	for k, v := range f.members {
		out[k] = map[string]pgstore.RadarDigestMember{}
		for m, st := range v {
			out[k][m] = st
		}
	}
	return out, nil
}

// ApplyRadarDigestRun 模拟 pgstore 语义：跌出标 in_prev=false、cur upsert true、
// pushed 盖 now、删 ∉cur 且未在册且（NULL 或超 24h）过期行。
func (f *fakeRadarStore) ApplyRadarDigestRun(_ context.Context, cur map[string][]string, pushed map[string][]string, now time.Time) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied = true
	for kind, members := range cur {
		if f.members[kind] == nil {
			f.members[kind] = map[string]pgstore.RadarDigestMember{}
		}
		for m, st := range f.members[kind] {
			st.InPrevSet = false
			f.members[kind][m] = st
		}
		for _, m := range members {
			st := f.members[kind][m]
			st.InPrevSet = true
			f.members[kind][m] = st
		}
		for _, m := range pushed[kind] {
			st := f.members[kind][m]
			ts := now
			st.LastPushedAt = &ts
			f.members[kind][m] = st
		}
		for m, st := range f.members[kind] {
			if !st.InPrevSet && (st.LastPushedAt == nil || st.LastPushedAt.Before(now.Add(-24*time.Hour))) {
				delete(f.members[kind], m)
			}
		}
	}
	return nil
}

// digestRound 一轮 digest 生命周期：pushDigestNewMembers 组行 + pushAndBookkeep 记账。
func digestRound(t *testing.T, fs *fakeRadarStore, kind string, curKeys []string, now time.Time) []string {
	t.Helper()
	stored, err := fs.GetRadarDigestMembers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	pending := map[string][]string{}
	pushDigestNewMembers(stored[kind], pending, &lines, kind, curKeys, func(m string) string { return m }, now)
	cur := map[string][]string{kind: curKeys}
	if len(lines) == 0 {
		// 零行轮仍记账（与 main.go else 分支同语义：in_prev 清零+过期清理）。
		if err := fs.ApplyRadarDigestRun(context.Background(), cur, nil, now); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	if err := pushAndBookkeep(context.Background(), fs,
		func(_ context.Context, l []string) error { fs.sentLines = append(fs.sentLines, l...); return nil },
		lines, cur, pending, nil, now); err != nil {
		t.Fatal(err)
	}
	return lines
}

// 锚 A：成员连续在册不重报——第一轮推 {A,B}，第二轮 cur 仍 {A,B} → 零行。
func TestD392_AnchorA_InSetNoRepublish(t *testing.T) {
	fs := newFakeRadarStore()
	now := time.Now()
	if lines := digestRound(t, fs, "real_leg", []string{"A", "B"}, now); len(lines) != 1 {
		t.Fatalf("首轮全新入应推 1 行: %v", lines)
	}
	if lines := digestRound(t, fs, "real_leg", []string{"A", "B"}, now.Add(time.Hour)); len(lines) != 0 {
		t.Fatalf("在册集合不变应零行: %v", lines)
	}
}

// 锚 B：新入才报——第二轮 cur {A,B,C} → 行只含 C + 「另 2 在册」。
func TestD392_AnchorB_NewOnly(t *testing.T) {
	fs := newFakeRadarStore()
	now := time.Now()
	digestRound(t, fs, "class_filtered", []string{"A", "B"}, now)
	lines := digestRound(t, fs, "class_filtered", []string{"A", "B", "C"}, now.Add(time.Hour))
	if len(lines) != 1 {
		t.Fatalf("应 1 行: %v", lines)
	}
	if !strings.Contains(lines[0], "C") || strings.Contains(lines[0], "A") || strings.Contains(lines[0], "B,") {
		t.Fatalf("行应只含新入 C: %s", lines[0])
	}
	if !strings.Contains(lines[0], "另 2 在册") {
		t.Fatalf("行应带在册计数: %s", lines[0])
	}
}

// 锚 C：flap 压制——C 被推后跌出、24h 内回归 → 不推。
func TestD392_AnchorC_FlapSuppressed(t *testing.T) {
	fs := newFakeRadarStore()
	now := time.Now()
	digestRound(t, fs, "suspect", []string{"C"}, now)
	digestRound(t, fs, "suspect", nil, now.Add(time.Hour)) // C 跌出（last_pushed 保留）
	lines := digestRound(t, fs, "suspect", []string{"C"}, now.Add(2*time.Hour))
	if len(lines) != 0 {
		t.Fatalf("24h 内回归应压: %v", lines)
	}
}

// 锚 D：久别重报——C last_pushed>24h 后回归 → 报。
func TestD392_AnchorD_ReentryAfterTTL(t *testing.T) {
	fs := newFakeRadarStore()
	now := time.Now()
	digestRound(t, fs, "suspect", []string{"C"}, now)
	digestRound(t, fs, "suspect", nil, now.Add(time.Hour))
	lines := digestRound(t, fs, "suspect", []string{"C"}, now.Add(26*time.Hour))
	if len(lines) != 1 || !strings.Contains(lines[0], "C") {
		t.Fatalf("跌出 >24h 回归应报: %v", lines)
	}
}

// 锚 F：ApplyRadarDigestRun 注入失败 → error 上抛、状态不落（R0-A3 语义回归）。
func TestD392_AnchorF_ApplyFailurePropagates(t *testing.T) {
	fs := newFakeRadarStore()
	fs.applyErr = errors.New("pg down")
	now := time.Now()
	stored, _ := fs.GetRadarDigestMembers(context.Background())
	var lines []string
	pending := map[string][]string{}
	pushDigestNewMembers(stored["suspect"], pending, &lines, "suspect", []string{"A"}, func(m string) string { return m }, now)
	if len(lines) != 1 {
		t.Fatal("全新入应组行")
	}
	okSend := func(context.Context, []string) error { return nil }
	err := pushAndBookkeep(context.Background(), fs, okSend, lines, map[string][]string{"suspect": {"A"}}, pending, nil, now)
	if err == nil {
		t.Fatal("ApplyRadarDigestRun 失败须上抛 error")
	}
	if fs.applied || len(fs.members) != 0 {
		t.Fatalf("失败不应落成员态: %+v", fs.members)
	}
}

// 锚 G（缺陷 2 回归）：C 在册被推 → 跌出后该类连续空集轮（in_prev 清零路径）→
// >24h 回归报、<24h 回归压。
func TestD392_AnchorG_EmptyRoundsClearInPrev(t *testing.T) {
	fs := newFakeRadarStore()
	now := time.Now()
	digestRound(t, fs, "legless_band", []string{"C"}, now)
	// 连续两轮空集：in_prev 必须被清（旧缺陷=只在 len>0 时记账会残留误压）
	digestRound(t, fs, "legless_band", nil, now.Add(time.Hour))
	digestRound(t, fs, "legless_band", nil, now.Add(2*time.Hour))
	if st := fs.members["legless_band"]["C"]; st.InPrevSet {
		t.Fatal("空集轮后 in_prev_set 应清零")
	}
	// <24h 回归压（flap）
	if lines := digestRound(t, fs, "legless_band", []string{"C"}, now.Add(3*time.Hour)); len(lines) != 0 {
		t.Fatalf("<24h 回归应压: %v", lines)
	}
	// 再跌出 → >24h 自首次推后起算回归报（last_pushed 保留首轮）
	digestRound(t, fs, "legless_band", nil, now.Add(4*time.Hour))
	lines := digestRound(t, fs, "legless_band", []string{"C"}, now.Add(25*time.Hour))
	if len(lines) != 1 {
		t.Fatalf(">24h 回归应报: %v", lines)
	}
}

// 锚 H（缺陷 1 回归）：legless 新入成员盖冷却戳、被压成员不盖。
func TestD392_AnchorH_LeglessTouchGranular(t *testing.T) {
	now := time.Now()
	// 预置：OLD 上轮在册且推过；NEW 首次进场。直接走成员级谓词验证 pending 拆分。
	stored := map[string]pgstore.RadarDigestMember{
		"OLD": {InPrevSet: true, LastPushedAt: ptrTime(now.Add(-time.Hour))},
	}
	var lines []string
	pending := map[string][]string{}
	pushDigestNewMembers(stored, pending, &lines, "legless_band", []string{"OLD", "NEW"},
		func(m string) string { return m }, now)
	if len(pending["legless_band"]) != 1 || pending["legless_band"][0] != "NEW" {
		t.Fatalf("pending 应只含新入 NEW: %v", pending["legless_band"])
	}
	// 冷却戳模拟：digest.go 只给 pending 新入成员盖戳
	pushedSet := map[string]bool{"NEW": true}
	var touches []alertTouch
	for _, sym := range []string{"OLD", "NEW"} {
		if pushedSet[sym] {
			touches = append(touches, alertTouch{"binance", sym})
		}
	}
	if len(touches) != 1 || touches[0].symbol != "NEW" {
		t.Fatalf("冷却戳应只盖新入 NEW: %v", touches)
	}
}

// 锚 E 语义回归（成员态版）：发送失败 → 整批丢 + 成员态零写 + 冷却戳零写。
func TestD392_SendFailureNoBookkeeping(t *testing.T) {
	fs := newFakeRadarStore()
	failSend := func(context.Context, []string) error { return errors.New("feishu 500") }
	err := pushAndBookkeep(context.Background(), fs, failSend, []string{"edge"},
		map[string][]string{"suspect": {"A"}}, map[string][]string{"suspect": {"A"}},
		[]alertTouch{{"binance", "BTCUSDT"}}, time.Now())
	if err != nil {
		t.Fatalf("发送失败不应上抛（整批丢、下轮重试）: %v", err)
	}
	if fs.applied || len(fs.touches) != 0 {
		t.Fatalf("发送失败成员态/冷却戳不应落: applied=%v touches=%v", fs.applied, fs.touches)
	}
}

// 锚 F 语义回归（成员态版）：real_leg pair 身份不含费率——费率漂移成员键不变
// 在册不重报；venue 换=新成员进场报。
func TestD392_RateDriftNoRepublish(t *testing.T) {
	p1 := divPair{Canon: "BTC", ShortVenue: "binance", LongVenue: "okx", FundShort: 10.0, FundLong: -5.0}
	p2 := p1
	p2.FundShort, p2.FundLong = 13.0, -3.5
	if realLegKey(p1) != realLegKey(p2) {
		t.Fatal("pair 身份键不应含费率")
	}
	fs := newFakeRadarStore()
	now := time.Now()
	digestRound(t, fs, "real_leg", []string{realLegKey(p1)}, now)
	if lines := digestRound(t, fs, "real_leg", []string{realLegKey(p2)}, now.Add(time.Hour)); len(lines) != 0 {
		t.Fatal("费率漂移 pair 身份不变应不推（R0-A1 选项 a 取舍）")
	}
	p3 := divPair{Canon: "BTC", ShortVenue: "gate", LongVenue: "okx"}
	if lines := digestRound(t, fs, "real_leg", []string{realLegKey(p1), realLegKey(p3)}, now.Add(2*time.Hour)); len(lines) != 1 {
		t.Fatal("pair 新成员进场应推")
	}
}
