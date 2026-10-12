// D-138 测试锚：回放逆序——DESC 源集逆序送出，前端 upsertById 头插落回 DESC。
package dashboard

import (
	"context"
	"testing"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

// recordSender 录制 Send 帧序（updateSender 接口实现）。
type recordSender struct {
	frames []*v1.StreamUpdatesResponse
	err    error
}

func (r *recordSender) Send(resp *v1.StreamUpdatesResponse) error {
	if r.err != nil {
		return r.err
	}
	r.frames = append(r.frames, resp)
	return nil
}

// upsertByIdMirror 前端 upsertById 头插语义镜像（shared.ts:6 同式——已存在替换、不存在 prepend）。
func upsertByIdMirror(ids []any, id any) []any {
	for i, x := range ids {
		if x == id {
			ids[i] = id
			return ids
		}
	}
	return append([]any{id}, ids...)
}

// TestD138_AlertsReplayOrder DESC 源（ts 新→旧）→ 发送序=ASC，镜像头插后终序=DESC。
func TestD138_AlertsReplayOrder(t *testing.T) {
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	// ListAlerts ORDER BY ts DESC：id 大=新
	alerts := []pgstore.Alert{
		{ID: 3, Ts: base, Level: "warn", Message: "newest"},
		{ID: 2, Ts: base.Add(-time.Hour), Level: "warn", Message: "mid"},
		{ID: 1, Ts: base.Add(-2 * time.Hour), Level: "warn", Message: "oldest"},
	}
	svc := &Service{}
	rec := &recordSender{}
	if err := svc.sendReplayAlerts(rec, alerts); err != nil {
		t.Fatalf("sendReplayAlerts: %v", err)
	}
	if len(rec.frames) != 3 {
		t.Fatalf("frames=%d, want 3", len(rec.frames))
	}
	// 发送序应为 1,2,3（ASC，最旧先发）
	for i, want := range []int64{1, 2, 3} {
		got := rec.frames[i].GetAlert().GetId()
		if got != want {
			t.Fatalf("frame[%d].id=%d, want %d（发送序应为 ASC）", i, got, want)
		}
	}
	// 前端头插镜像：回放完数组终序=DESC（最新置顶）
	var ids []any
	for _, f := range rec.frames {
		ids = upsertByIdMirror(ids, f.GetAlert().GetId())
	}
	for i, want := range []any{int64(3), int64(2), int64(1)} {
		if ids[i] != want {
			t.Fatalf("hydrated ids=%v, want [3 2 1]（DESC 置顶）", ids)
		}
	}
	// 实时帧仍头插（新告警 id=4 到顶）
	ids = upsertByIdMirror(ids, int64(4))
	if ids[0] != int64(4) {
		t.Fatalf("live upsert should prepend newest on top, got %v", ids)
	}
}

// TestD138_OppsReplayOrder net_bps DESC 源 → 镜像头插终序=DESC（最优置顶）。
func TestD138_OppsReplayOrder(t *testing.T) {
	opps := []pgstore.Opportunity{
		{ID: "best", Type: "SPREAD", VenueBuy: "a", VenueSell: "b", NetBps: 50},
		{ID: "mid", Type: "SPREAD", VenueBuy: "a", VenueSell: "b", NetBps: 30},
		{ID: "worst", Type: "SPREAD", VenueBuy: "a", VenueSell: "b", NetBps: 10},
	}
	svc := &Service{}
	rec := &recordSender{}
	ctx := context.Background()
	if err := svc.sendReplayOpps(ctx, rec, opps); err != nil {
		t.Fatalf("sendReplayOpps: %v", err)
	}
	if len(rec.frames) != 3 {
		t.Fatalf("frames=%d, want 3", len(rec.frames))
	}
	for i, want := range []string{"worst", "mid", "best"} {
		got := rec.frames[i].GetOpportunity().GetId()
		if got != want {
			t.Fatalf("frame[%d].id=%s, want %s（发送序应为 net_bps ASC）", i, got, want)
		}
	}
	var ids []any
	for _, f := range rec.frames {
		ids = upsertByIdMirror(ids, f.GetOpportunity().GetId())
	}
	if ids[0] != "best" {
		t.Fatalf("hydrated top=%v, want best（net_bps 最优置顶）", ids)
	}
}
