// d492_allowrules_admin_test.go —— D-492 A1–A7 断言锚：Add/DeleteAutotradeAllowRule
// （kind/value 校验+simulated 服务端推导+幂等去重+disabled 复用+审计+kickConfig）
// + ApproveCarryGraduationResponse.venue_gap_keys 纯 key 透出。库面锚需 ARB_TEST_PG_DSN。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"

	"connectrpc.com/connect"
)

// d492Store 真库 + d492 前缀清扫（rules/exchange_configs/broker_accounts/alerts）。
func d492Store(t *testing.T) (*pgstore.Store, *Service, *int) {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `
		DELETE FROM autotrade_allow_rules WHERE value LIKE 'd492%' OR value LIKE '492%';
		DELETE FROM exchange_configs WHERE venue LIKE 'd492%';
		DELETE FROM broker_accounts WHERE venue IN ('49200001','49200002','49200003');
		DELETE FROM alerts WHERE message LIKE '%d492%'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	kicked := 0
	svc := &Service{store: st, environment: "live"}
	svc.SetConfigKick(func() { kicked++ })
	return st, svc, &kicked
}

func addReq(kind, value, note string) *connect.Request[v1.AddAutotradeAllowRuleRequest] {
	return connect.NewRequest(&v1.AddAutotradeAllowRuleRequest{Kind: kind, Value: value, Note: note})
}

// A1：CEX venue 实盘行存在 → 插入 simulated=false enabled=true + info 审计 + kick 触发。
func TestD492_A1_AddVenueHappy(t *testing.T) {
	st, svc, kicked := d492Store(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret, simulated)
		VALUES ('d492cex','spot','k','s',false)`); err != nil {
		t.Fatalf("seed exchange_config: %v", err)
	}
	resp, err := svc.AddAutotradeAllowRule(ctx, addReq("venue", "d492cex:spot", "A1 开通"))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v msg=%q", resp.Msg.GetOk(), err, resp.Msg.GetError())
	}
	var sim, en bool
	if err := st.Pool().QueryRow(ctx,
		`SELECT simulated, enabled FROM autotrade_allow_rules WHERE id=$1`, resp.Msg.GetId()).Scan(&sim, &en); err != nil {
		t.Fatalf("row readback: %v", err)
	}
	if sim || !en {
		t.Fatalf("simulated=%v enabled=%v, want false/true", sim, en)
	}
	if *kicked == 0 {
		t.Error("kickConfig 未被触发（热载依赖）")
	}
	var alertN int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '%d492cex:spot%' AND message LIKE '%[whitelist]%'`).Scan(&alertN); err != nil {
		t.Fatalf("alert check: %v", err)
	}
	if alertN == 0 {
		t.Error("缺 info 审计告警行")
	}
}

// A2：同 key 重复加 → 幂等 ok 不新插；disabled 行 → reenabled_existing=true 复用翻转。
func TestD492_A2_IdempotentReenable(t *testing.T) {
	st, svc, _ := d492Store(t)
	ctx := context.Background()
	st.Pool().Exec(ctx, `INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret, simulated) VALUES ('d492dup','perp','k','s',false)`)

	r1, _ := svc.AddAutotradeAllowRule(ctx, addReq("venue", "d492dup:perp", "首插"))
	if !r1.Msg.GetOk() {
		t.Fatalf("首插失败: %q", r1.Msg.GetError())
	}
	r2, _ := svc.AddAutotradeAllowRule(ctx, addReq("venue", "d492dup:perp", "重复"))
	if !r2.Msg.GetOk() || r2.Msg.GetReenabledExisting() || !strings.Contains(r2.Msg.GetWarning(), "已存在") {
		t.Fatalf("重复加应幂等 ok+warning, got %+v", r2.Msg)
	}
	var n int
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM autotrade_allow_rules WHERE value='d492dup:perp'`).Scan(&n)
	if n != 1 {
		t.Fatalf("应只有 1 行，got %d", n)
	}
	if _, err := st.SetAutotradeAllowRuleEnabled(ctx, r1.Msg.GetId(), false, "停用"); err != nil {
		t.Fatalf("停用: %v", err)
	}
	r3, _ := svc.AddAutotradeAllowRule(ctx, addReq("venue", "d492dup:perp", "复用"))
	if !r3.Msg.GetOk() || !r3.Msg.GetReenabledExisting() || r3.Msg.GetId() != r1.Msg.GetId() {
		t.Fatalf("disabled 行应复用翻转, got %+v", r3.Msg)
	}
}

// A3：无 exchange_configs 行拒；live env 下仅有 simulated=t 配置也拒（禁取首行猜测）。
func TestD492_A3_NoConfigRowReject(t *testing.T) {
	st, svc, _ := d492Store(t)
	ctx := context.Background()
	if r, _ := svc.AddAutotradeAllowRule(ctx, addReq("venue", "d492none:spot", "无配置")); r.Msg.GetOk() {
		t.Fatal("无配置行应拒")
	}
	st.Pool().Exec(ctx, `INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret, simulated) VALUES ('d492sim','spot','k','s',true)`)
	if r, _ := svc.AddAutotradeAllowRule(ctx, addReq("venue", "d492sim:spot", "demo行live拒")); r.Msg.GetOk() {
		t.Fatal("live 下仅有模拟配置应拒")
	}
	// demo env 下同一 venue 应推导 simulated=true 放行
	svc.environment = "demo"
	if r, _ := svc.AddAutotradeAllowRule(ctx, addReq("venue", "d492sim:spot", "demo放行")); !r.Msg.GetOk() {
		t.Fatalf("demo env 应放行模拟配置, err=%q", r.Msg.GetError())
	}
}

// A4：MT venue 推导——真实盘账号/live ok；无账号/仅MT4/is_real NULL 三路拒。
func TestD492_A4_MTVenueDerive(t *testing.T) {
	st, svc, _ := d492Store(t)
	ctx := context.Background()
	seed := func(venue, venueType string, isReal *bool) {
		if _, err := st.Pool().Exec(ctx, `
			INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
			VALUES ($1,$2,'h',1,'p',$3)`, venue, venueType, isReal); err != nil {
			t.Fatalf("seed account %s: %v", venue, err)
		}
	}
	tr, fa := true, false
	seed("49200001", "mt5", &tr)
	seed("49200002", "mt4", &fa) // 仅 MT4 账号
	seed("49200003", "mt5", nil) // is_real 未知

	if r, _ := svc.AddAutotradeAllowRule(ctx, addReq("venue", "49200001:mt5", "实盘MT5")); !r.Msg.GetOk() {
		t.Fatalf("实盘 MT5 账号应放行, err=%q", r.Msg.GetError())
	}
	var sim bool
	st.Pool().QueryRow(ctx, `SELECT simulated FROM autotrade_allow_rules WHERE value='49200001:mt5'`).Scan(&sim)
	if sim {
		t.Fatal("实盘账号应推导 simulated=false")
	}
	for _, v := range []string{"49209999:mt5", "49200002:mt5", "49200003:mt5"} {
		if r, _ := svc.AddAutotradeAllowRule(ctx, addReq("venue", v, "应拒")); r.Msg.GetOk() {
			t.Fatalf("%s 应拒（无账号/仅MT4/is_real未知）", v)
		}
	}
}

// A5：canonical 无采集记录 → ok+warning 透出；kind/value/note 校验三路拒。
func TestD492_A5_CanonicalAndValidation(t *testing.T) {
	_, svc, _ := d492Store(t)
	ctx := context.Background()
	r, err := svc.AddAutotradeAllowRule(ctx, addReq("canonical", "D492ZZZ", "新标的"))
	if err != nil || !r.Msg.GetOk() {
		t.Fatalf("canonical 无采集应 warn 不拒, ok=%v err=%v msg=%q", r.Msg.GetOk(), err, r.Msg.GetError())
	}
	if !strings.Contains(r.Msg.GetWarning(), "无采集记录") {
		t.Fatalf("缺 warning, got %q", r.Msg.GetWarning())
	}
	for _, c := range [][3]string{
		{"bad", "D492ZZZ", "n"},        // kind 非法
		{"venue", "Binance:spot", "n"}, // 大写拒（禁静默规范化）
		{"venue", "d492x:mt4", "n"},    // mt4 已退役
		{"canonical", "d492zzz", "n"},  // 小写拒
		{"canonical", "D492ZZZ", "  "}, // note 空拒
	} {
		if rr, _ := svc.AddAutotradeAllowRule(ctx, addReq(c[0], c[1], c[2])); rr.Msg.GetOk() {
			t.Fatalf("应拒: kind=%q value=%q", c[0], c[1])
		}
	}
}

// A6：enabled 行禁删；disabled 行删 + 审计。
func TestD492_A6_DeleteGuard(t *testing.T) {
	st, svc, _ := d492Store(t)
	ctx := context.Background()
	var id int64
	st.Pool().QueryRow(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated, note)
		VALUES ('canonical','D492DEL',true,false,'') RETURNING id`).Scan(&id)
	if r, _ := svc.DeleteAutotradeAllowRule(ctx, connect.NewRequest(&v1.DeleteAutotradeAllowRuleRequest{Id: id, Note: "试删"})); r.Msg.GetOk() {
		t.Fatal("enabled 行应拒删")
	}
	if _, err := st.SetAutotradeAllowRuleEnabled(ctx, id, false, ""); err != nil {
		t.Fatalf("停用: %v", err)
	}
	r, err := svc.DeleteAutotradeAllowRule(ctx, connect.NewRequest(&v1.DeleteAutotradeAllowRuleRequest{Id: id, Note: "删除确认"}))
	if err != nil || !r.Msg.GetOk() {
		t.Fatalf("disabled 行应删成功, ok=%v err=%v msg=%q", r.Msg.GetOk(), err, r.Msg.GetError())
	}
	row, _ := st.AutotradeAllowRuleByID(ctx, id)
	if row != nil {
		t.Fatal("删除后行仍在")
	}
	var alertN int
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE '%D492DEL%' AND message LIKE '%delete%'`).Scan(&alertN)
	if alertN == 0 {
		t.Error("缺删除审计告警")
	}
}

// A7：approve dry_run 响应 venue_gap_keys 纯 key 列表（gap venue 在内、capWarns 不混入）。
// fixture：1279967:mt5 不在白名单（allow={gate:perp}）→ keys=[1279967:mt5]。
func TestD492_A7_VenueGapKeys(t *testing.T) {
	f, s := d490Fixture(d490Snaps(2000, 1000, "USD", time.Minute), 0.01)
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, DryRun: true, FundingUsd: d490f(100),
	}))
	if err != nil || !resp.Msg.GetOk() {
		t.Fatalf("dry_run ok=%v err=%v msg=%q", resp.Msg.GetOk(), err, resp.Msg.GetError())
	}
	keys := resp.Msg.GetVenueGapKeys()
	if len(keys) != 1 || keys[0] != "1279967:mt5" {
		t.Fatalf("venue_gap_keys = %v, want [1279967:mt5]", keys)
	}
	for _, k := range keys {
		found := false
		for _, g := range resp.Msg.GetVenueGaps() {
			if g == k {
				found = true
			}
		}
		if !found {
			t.Errorf("key %q 应在 venue_gaps 内", k)
		}
	}
	if len(resp.Msg.GetVenueGaps()) <= len(keys) {
		t.Fatalf("venue_gaps 应含 capWarns 句串多于 keys: %v", resp.Msg.GetVenueGaps())
	}
	_ = f
}

// R1/R2：dry_run 裸探针豁免——无 sizing/显式入参放行回 ok+venue_gap_keys。
func TestD492_R1_BareProbeGapKeys(t *testing.T) {
	// perp_perp：venue "gate×okx"，allow={gate:perp} → keys=[okx:perp]（R2 全形口径）。
	fee, entryB, lastB := 0.5, 10.0, 12.0
	f := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 6, PaperID: 43, VenueA: "gate", VenueB: "okx", Symbol: "BTC", Structure: "perp_perp", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 43, VenueA: "gate", VenueB: "okx", Structure: "perp_perp",
			Status: "active", AccruedUSD: 5.0, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB},
	}
	s := &Service{gradStore: f, xassetReloader: &fakeReloader{}, allowVenuesSource: func(context.Context) (map[string]bool, error) {
		return map[string]bool{"gate:perp": true}, nil
	}}
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{GraduationId: 6, DryRun: true}))
	if err != nil || !resp.Msg.GetOk() {
		t.Fatalf("perp_perp 裸探针 ok=%v err=%v msg=%q", resp.Msg.GetOk(), err, resp.Msg.GetError())
	}
	if keys := resp.Msg.GetVenueGapKeys(); len(keys) != 1 || keys[0] != "okx:perp" {
		t.Fatalf("perp_perp keys=%v, want [okx:perp]", keys)
	}
	if f.combo != nil || f.decision != "" {
		t.Error("dry_run 零副作用：combo/decision 均不得写")
	}

	// spot_carry：venue "binance" 双腿口径，allow={binance:perp} → keys=[binance:spot]。
	f2 := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 7, PaperID: 44, VenueA: "binance", Symbol: "SKLUSDT", Structure: "spot_carry", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 44, VenueA: "binance", Symbol: "SKLUSDT", Structure: "spot_carry",
			Status: "active", AccruedUSD: 5.0, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB},
	}
	s2 := &Service{gradStore: f2, xassetReloader: &fakeReloader{}, allowVenuesSource: func(context.Context) (map[string]bool, error) {
		return map[string]bool{"binance:perp": true}, nil
	}}
	resp2, err := s2.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{GraduationId: 7, DryRun: true}))
	if err != nil || !resp2.Msg.GetOk() {
		t.Fatalf("spot_carry 裸探针 ok=%v err=%v msg=%q", resp2.Msg.GetOk(), err, resp2.Msg.GetError())
	}
	if keys := resp2.Msg.GetVenueGapKeys(); len(keys) != 1 || keys[0] != "binance:spot" {
		t.Fatalf("spot_carry keys=%v, want [binance:spot]", keys)
	}
}
