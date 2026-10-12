package autotrade

// venue/canonical 白名单热读——holder 注入判定正确、ReloadAllowRules 换指针
// 新值生效、空 venue 集全拒、canonical 空集放行（D-122 回归锚）。
import (
	"context"
	"strings"
	"testing"

	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
)

func allowTestEngine(venues, canonicals map[string]bool) *Engine {
	e := &Engine{cfg: gate.Config{AllowVenues: venues, AllowCanonicals: canonicals}}
	e.allowRules.Store(&allowRules{venues: venues, canonicals: canonicals})
	return e
}

func TestD443_AllowRuleReject(t *testing.T) {
	opp := pgstore.Opportunity{Canonical: "BTCUSDT", VenueBuy: "a:mt5", VenueSell: "okx:perp"}
	e := allowTestEngine(
		map[string]bool{"a:mt5": true, "okx:perp": true},
		map[string]bool{"BTCUSDT": true})
	if r := e.allowRuleReject(opp); r != "" {
		t.Errorf("放行组合被误拒: %s", r)
	}
	if r := e.allowRuleReject(pgstore.Opportunity{Canonical: "BTCUSDT", VenueBuy: "x:mt5", VenueSell: "okx:perp"}); !strings.Contains(r, "venue not in allowlist") {
		t.Errorf("venue 外组合应拒, got %q", r)
	}
	if r := e.allowRuleReject(pgstore.Opportunity{Canonical: "ETHUSDT", VenueBuy: "a:mt5", VenueSell: "okx:perp"}); !strings.Contains(r, "canonical not in allowlist") {
		t.Errorf("canonical 外组合应拒, got %q", r)
	}
}

func TestD443_EmptyVenuesRejectAll(t *testing.T) {
	e := allowTestEngine(map[string]bool{}, map[string]bool{})
	if r := e.allowRuleReject(pgstore.Opportunity{Canonical: "BTCUSDT", VenueBuy: "a:mt5", VenueSell: "b:perp"}); r == "" {
		t.Error("空 venue 集=全拒（fail-closed 最保守），却放行")
	}
}

func TestD443_EmptyCanonicalsAllowAll(t *testing.T) {
	e := allowTestEngine(map[string]bool{"a:mt5": true, "b:perp": true}, map[string]bool{})
	if r := e.allowRuleReject(pgstore.Opportunity{Canonical: "ANYTHING", VenueBuy: "a:mt5", VenueSell: "b:perp"}); r != "" {
		t.Errorf("canonical 空集=全放行（D-122），却拒: %s", r)
	}
}

func TestD443_ReloadAllowRules(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_allow_rules`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated) VALUES
		('venue','new:mt5',true,false),('canonical','XAUUSD',true,false)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &Engine{store: s, cfg: gate.Config{Environment: "live", AllowVenues: map[string]bool{"old:mt5": true}}}
	e.allowRules.Store(&allowRules{venues: e.cfg.AllowVenues, canonicals: map[string]bool{}})
	if err := e.ReloadAllowRules(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if r := e.allowRuleReject(pgstore.Opportunity{Canonical: "XAUUSD", VenueBuy: "new:mt5", VenueSell: "new:mt5"}); r != "" {
		t.Errorf("reload 后新行应生效放行, got %q", r)
	}
	if r := e.allowRuleReject(pgstore.Opportunity{Canonical: "XAUUSD", VenueBuy: "old:mt5", VenueSell: "new:mt5"}); r == "" {
		t.Error("reload 后旧值应失效（指针整体替换非合并）")
	}
	// simulated 闸：live（simulated=false）读不到 demo 行
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated) VALUES ('venue','demo:mt5',true,true)`); err != nil {
		t.Fatalf("seed demo: %v", err)
	}
	if err := e.ReloadAllowRules(ctx); err != nil {
		t.Fatalf("reload2: %v", err)
	}
	if e.allowVenues()["demo:mt5"] {
		t.Error("live 引擎不应读到 simulated=true 行")
	}
}
