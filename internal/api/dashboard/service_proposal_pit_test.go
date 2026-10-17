// service_proposal_pit_test.go —— 坑签名透出续段①验收锚：提案行 payoff 签名
// 命中断言（dci_buy_low→naked_short_vol→dci-sell-put-negative-skew ⚠）+
// 零命中如实空数组（禁线：呈现非闸，不造标）。econkb 目录指向仓库经济库
// （测试 cwd 相对径，同 tools/noxkb econDirConst 形态）。
package dashboard

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"nox/internal/econkb"
	dashv1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// econDirForTest 经济库绝对径（runtime.Caller 推源码位——相对径在本机 go test
// 进程内解析异常实证：绝对径 OK 而 ../../docs ERR，故不赌相对径）。
func econDirForTest() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "经济经验库")
}

func TestListProposalsPitHits(t *testing.T) {
	t.Setenv("NOX_ECONKB_DIR", econDirForTest())
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	// 定点清理+唯一键（禁 TRUNCATE 共享表——跨包并行连坐纪律）。
	for _, q := range []string{
		`DELETE FROM fin_proposal WHERE template_id IN ('tpit_dci','tpit_earn')`,
		`DELETE FROM fin_opp_template WHERE id IN ('tpit_dci','tpit_earn')`,
	} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM fin_proposal WHERE template_id IN ('tpit_dci','tpit_earn')`,
			`DELETE FROM fin_opp_template WHERE id IN ('tpit_dci','tpit_earn')`,
		} {
			_, _ = s.Pool().Exec(context.Background(), q)
		}
	})
	// 裁判模板：tpit_dci=naked_short_vol（dci-sell-put-negative-skew 签名域）、
	// tpit_earn=yield_on_holdings（无 payoff 键签名命中面）。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_opp_template (id, mechanism, net_apr, exec_track, risk_level, enabled, payoff_class)
		VALUES ('tpit_dci','x','1','auto','low',true,'naked_short_vol'),
		       ('tpit_earn','x','1','auto','low',true,'yield_on_holdings')`); err != nil {
		t.Fatalf("seed templates: %v", err)
	}
	for _, p := range []struct{ tmpl, venue string }{
		{"tpit_dci", "okx"},
		{"tpit_earn", "okx"},
	} {
		if _, _, err := s.CreateProposal(ctx, &pgstore.Proposal{
			TemplateID: p.tmpl, ExecTrack: "auto", Venue: p.venue,
			Params: map[string]any{"venue": p.venue},
		}); err != nil {
			t.Fatalf("seed proposal %s: %v", p.tmpl, err)
		}
	}
	svc := &Service{store: s}
	resp, err := svc.ListProposals(ctx, connect.NewRequest(&dashv1.ListProposalsRequest{}))
	if err != nil {
		t.Fatalf("ListProposals: %v", err)
	}
	byTmpl := map[string][]string{}
	for _, pr := range resp.Msg.Proposals {
		byTmpl[pr.TemplateId] = pr.PitHits
	}
	dciHits := byTmpl["tpit_dci"]
	if len(dciHits) == 0 || !slices.ContainsFunc(dciHits, func(h string) bool {
		return strings.Contains(h, "dci-sell-put-negative-skew")
	}) {
		t.Errorf("naked_short_vol 提案须命中 dci-sell-put-negative-skew 签名: %v", dciHits)
	}
	if hits := byTmpl["tpit_earn"]; len(hits) != 0 {
		t.Errorf("yield_on_holdings 提案零命中须如实空数组（不造标）: %v", hits)
	}
}

// MatchHits 经济库直读回归锚（payoff_class 键经提案面馈入后命中语义不变——
// 真库条目 dci-sell-put-negative-skew 的 match 段=payoff_class: naked_short_vol）。
func TestEconMatchPayoffClassKey(t *testing.T) {
	t.Setenv("NOX_ECONKB_DIR", econDirForTest())
	hits := econkb.MatchHits(econkb.Dir(), econkb.Input{PayoffClass: "naked_short_vol"})
	found := false
	for _, h := range hits {
		if strings.Contains(h.ID, "dci-sell-put-negative-skew") {
			found = true
		}
	}
	if !found {
		t.Fatalf("payoff_class=naked_short_vol 须命中 dci-sell-put-negative-skew: %+v", hits)
	}
}
