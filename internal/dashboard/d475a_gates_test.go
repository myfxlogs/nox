// d475a_gates_test.go —— D-475a S3/S4 验收锚：
// A3 approve 双闸（flag 复核在 isCfdStructure 分叉前 + 敞口三态语义）；
// A4 吊销编排（写旗即踢 + 组合吊销 + 在管仓 warn 列 posID + 幂等）。
package dashboard

import (
	"context"
	"strings"
	"testing"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
	"connectrpc.com/connect"
)

func d475aFlag(venue, symbol, level string) pgstore.VenueSymbolFlag {
	return pgstore.VenueSymbolFlag{Venue: venue, Symbol: symbol, Level: level, Reason: "测试坑"}
}

// TestD475a_A3_FlagGate_BeforeBranch blocked 旗在非 cfd 结构（spot_carry）上即拒——
// 证明复核闸在 isCfdStructure 分叉前（只进 cfd 分支=设计漏盖）。
func TestD475a_A3_FlagGate_BeforeBranch(t *testing.T) {
	f, s := gradFixture("spot_carry")
	f.activeFlags = map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("1279967×gate", "BTC"): d475aFlag("1279967×gate", "BTC", "blocked"),
	}
	resp, _ := s.ApproveCarryGraduation(context.Background(), approveReq())
	if resp.Msg.Ok || !strings.Contains(resp.Msg.Error, "blocked flag") {
		t.Fatalf("组合串 blocked 旗应拒批，ok=%v err=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	if f.decision != "" {
		t.Fatal("拒批后不得落账")
	}
	// warn 旗→放行但 note 带 flag_warn 前缀。
	f2, s2 := gradFixture("spot_carry")
	f2.activeFlags = map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("gate", "BTC"): d475aFlag("gate", "BTC", "warn"),
	}
	resp2, _ := s2.ApproveCarryGraduation(context.Background(), approveReq())
	if !resp2.Msg.Ok {
		t.Fatalf("warn 旗应放行，err=%s", resp2.Msg.Error)
	}
	if !strings.HasPrefix(f2.decisionNote, "flag_warn:") && !strings.Contains(f2.decisionNote, "flag_warn:") {
		t.Fatalf("decided_note 应带 flag_warn 前缀，got %q", f2.decisionNote)
	}
}

// TestD475a_A3_ExposureCap 三态：cap=0 冻结新批准；超限拒；未设闸放行+warn 透出；
// NotionalUSD 优先于 MaxGrossUSD；enabled=false 放行。
func TestD475a_A3_ExposureCap(t *testing.T) {
	// cap=0 → 拒（冻结新批准，不吊销存量）。
	f, s := gradFixture("long_cfd_short_perp")
	f.caps = map[string]pgstore.VenueExposureCap{"gate:perp": {VenueKey: "gate:perp", CapUSD: 0, Enabled: true}}
	resp, _ := s.ApproveCarryGraduation(context.Background(), approveReq())
	if resp.Msg.Ok || !strings.Contains(resp.Msg.Error, "cap=0") {
		t.Fatalf("cap=0 应拒批，ok=%v err=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	if f.combo != nil {
		t.Fatal("冻结批准不得写 combo")
	}
	// enabled=false → 放行（闸停用行在档）。
	f2, s2 := gradFixture("long_cfd_short_perp")
	f2.caps = map[string]pgstore.VenueExposureCap{"gate:perp": {VenueKey: "gate:perp", Enabled: false}}
	resp2, _ := s2.ApproveCarryGraduation(context.Background(), approveReq())
	if !resp2.Msg.Ok {
		t.Fatalf("enabled=false 应放行，err=%s", resp2.Msg.Error)
	}
	// 未设闸 → 放行 + venue_gaps 如实透出 warn（非静默）。
	_, s3 := gradFixture("long_cfd_short_perp")
	resp3, _ := s3.ApproveCarryGraduation(context.Background(), approveReq())
	if !resp3.Msg.Ok {
		t.Fatalf("未设闸应放行，err=%s", resp3.Msg.Error)
	}
	foundWarn := false
	for _, g := range resp3.Msg.VenueGaps {
		if strings.Contains(g, "未设敞口闸") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("venue_gaps 应含未设闸 warn，got %v", resp3.Msg.VenueGaps)
	}
	// 超限 → 拒（存量 sum+proposed>cap；fake allowRows 默认 gate:perp sell 腿挂 5000）。
	f4, s4 := gradFixture("long_cfd_short_perp")
	f4.caps = map[string]pgstore.VenueExposureCap{
		"1279967:mt5": {VenueKey: "1279967:mt5", CapUSD: 1e9, Enabled: true},
		"gate:perp":   {VenueKey: "gate:perp", CapUSD: 8000, Enabled: true}, // 5000 存量+5000 本次>8000
	}
	resp4, _ := s4.ApproveCarryGraduation(context.Background(), approveReq())
	if resp4.Msg.Ok || !strings.Contains(resp4.Msg.Error, "敞口超限") {
		t.Fatalf("超限应拒，ok=%v err=%s", resp4.Msg.Ok, resp4.Msg.Error)
	}
	// cap 内 → 批。
	f5, s5 := gradFixture("long_cfd_short_perp")
	f5.caps = map[string]pgstore.VenueExposureCap{"gate:perp": {VenueKey: "gate:perp", CapUSD: 1e9, Enabled: true}}
	if resp5, _ := s5.ApproveCarryGraduation(context.Background(), approveReq()); !resp5.Msg.Ok {
		t.Fatalf("cap 内应批，err=%s", resp5.Msg.Error)
	}
	// NotionalUSD 优先：req 传 notional=100（<<cap）即使 gross=5000+存量=超限也批。
	f6, s6 := gradFixture("long_cfd_short_perp")
	f6.caps = map[string]pgstore.VenueExposureCap{"gate:perp": {VenueKey: "gate:perp", CapUSD: 5100, Enabled: true}}
	notional := 100.0
	resp6, _ := s6.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, MaxLongQty: 0.01, MaxShortQty: 1000, MaxGrossUsd: 5000, NotionalUsd: &notional,
	}))
	if !resp6.Msg.Ok {
		t.Fatalf("NotionalUSD 优先兜底应批（100+5000≤5100），err=%s", resp6.Msg.Error)
	}
}

// TestD475a_A4_Revoke blocked 写旗即踢+组合吊销+在管仓 warn 列 posID+幂等。
func TestD475a_A4_Revoke(t *testing.T) {
	f, s := gradFixture("spot_carry")
	kicked := 0
	s.SetConfigKick(func() { kicked++ })
	f.disableRet = 2
	f.openPos = map[string][]int64{"okx": {502, 511}} // grad venue=1279967×gate→两段各查
	resp, err := s.RejectCarryGraduation(context.Background(), connect.NewRequest(&v1.RejectCarryGraduationRequest{
		GraduationId: 5, Note: "坑", WriteFlag: true, FlagLevel: "blocked",
	}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v", resp.Msg.Ok, err)
	}
	if kicked == 0 {
		t.Fatal("写 blocked 旗必须即踢 kickConfig（不等 5min ticker）")
	}
	if len(f.disabledCalls) != 2 || f.disabledCalls[0] != "1279967|BTC" || f.disabledCalls[1] != "gate|BTC" {
		t.Fatalf("吊销应逐 venue 段调用，got %v", f.disabledCalls)
	}
	if f.alerts < 1 {
		t.Fatal("吊销行数>0 应落 warn 告警")
	}
	// warn 级不吊销不踢后的再写幂等（重复 disable 调用无副作用——fake 计数即可）。
	f.disableRet = 0
	s.revokeCombosForFlag(context.Background(), "1279967×gate", "BTC")
	if len(f.disabledCalls) != 4 { // 再调两段仍各一次（幂等=重复 disable 无害）
		t.Fatalf("幂等重放应仍走两段调用，got %v", f.disabledCalls)
	}
}
