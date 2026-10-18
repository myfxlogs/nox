package pgstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestD622_GapDispatchLine D-622 泛化面验收锚：业主请求类缺口推送文案末行=决策席
// 开工令+请求行 id；runtime 病灶推送不嵌令；ReopenConditionGap 复推同法。
func TestD622_GapDispatchLine(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	// 清场（sharedTestStore 同库跨轮残留——D-186 先例）。
	for _, subj := range []string{"d622_gap_perm", "d622_gap_cap", "d622_gap_rt"} {
		if _, err := s.pool.Exec(ctx, `DELETE FROM alerts WHERE message LIKE '%'||$1||'%'`, subj); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM fin_condition_gap WHERE subject=$1`, subj); err != nil {
			t.Fatal(err)
		}
	}
	// permission → 凭据/权限请求处置
	idPerm, err := s.OpenConditionGap(ctx, &ConditionGap{TemplateID: "t_d622_perm", Kind: "permission",
		Subject: "d622_gap_perm", RequestText: "请开通 X 权限"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestConditionGap(ctx, idPerm); err != nil {
		t.Fatal(err)
	}
	assertGapDispatchLastLine(t, s, "d622_gap_perm", idPerm)
	// capital → 资本请求处置
	idCap, err := s.OpenConditionGap(ctx, &ConditionGap{TemplateID: "t_d622_cap", Kind: "capital",
		Subject: "d622_gap_cap", RequestText: "需入金补弹药"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestConditionGap(ctx, idCap); err != nil {
		t.Fatal(err)
	}
	assertGapDispatchLastLine(t, s, "d622_gap_cap", idCap)
	// runtime 病灶 → 不嵌令（末行仍是正文，前缀非「请」）。kind CHECK（迁移 1023）
	// 已不容新插 runtime 行——legacy 存量径直调组装点覆盖。
	idRT, err := s.OpenConditionGap(ctx, &ConditionGap{TemplateID: "t_d622_rt", Kind: "executor",
		Subject: "d622_gap_rt", RequestText: "频率超限自愈中"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.pushGapAlert(ctx, idRT, ConditionGap{TemplateID: "t_d622_rt", Kind: "runtime",
		Subject: "d622_gap_rt", RequestText: "频率超限自愈中"}); err != nil {
		t.Fatal(err)
	}
	msg := latestGapAlert(t, s, "d622_gap_rt")
	if strings.Contains(msg, "[角色:决策]") || !strings.Contains(msg, "系统侧病灶") {
		t.Fatalf("runtime 病灶推送不应嵌转发令: %q", msg)
	}
	// ReopenConditionGap 同法：fulfilled→条件再失复推亦嵌令（若复推丢令=异键新行
	// 顶到最新→断言红；带令=同键窗抑仍指带令行）。
	if _, err := s.ResolveConditionGap(ctx, idPerm, true, "t"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReopenConditionGap(ctx, idPerm); err != nil {
		t.Fatal(err)
	}
	assertGapDispatchLastLine(t, s, "d622_gap_perm", idPerm)
}

// latestGapAlert 取含 subject 的最新告警文案。
func latestGapAlert(t *testing.T, s *Store, subject string) string {
	t.Helper()
	var msg string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT message FROM alerts WHERE message LIKE '%'||$1||'%' ORDER BY id DESC LIMIT 1`,
		subject).Scan(&msg); err != nil {
		t.Fatalf("gap alert 缺（subject=%s）: %v", subject, err)
	}
	return msg
}

// assertGapDispatchLastLine D-622 泛化面锚：末行=「[角色:决策] <主题>处置（读 STATE
// 本行+fin_condition_gap#<id>）」——删 pushGapAlert 转发行本断言必红。
func assertGapDispatchLastLine(t *testing.T, s *Store, subject string, id int64) {
	t.Helper()
	msg := latestGapAlert(t, s, subject)
	last := msg[strings.LastIndexByte(msg, '\n')+1:]
	if !strings.Contains(last, "[角色:决策]") ||
		!strings.Contains(last, "读 STATE 本行") ||
		!strings.Contains(last, fmt.Sprintf("fin_condition_gap#%d", id)) {
		t.Fatalf("subject=%s 告警末行应含决策席开工令+请求 id: %q", subject, last)
	}
	if i := strings.LastIndexByte(msg, '\n'); i <= 0 {
		t.Fatalf("转发令应为末行且前有正文行: %q", msg)
	}
}
