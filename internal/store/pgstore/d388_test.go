package pgstore

// D-388 S0.2：五 setter 对不存在 venue 返回 ErrBrokerAccountNotFound（UPDATE 0 行命中
// 不再静默返回 nil——前端停用点击的 2xx 假成功路径硬化）。锚 C 前置（RPC CodeNotFound
// 映射在 dashboard 层测）。

import (
	"context"
	"errors"
	"testing"

	"arb/internal/testutil"
)

func d388Store(t *testing.T) *Store {
	t.Helper()
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE broker_accounts`); err != nil {
		t.Fatalf("TRUNCATE broker_accounts: %v", err)
	}
	return s
}

// 不存在 venue：全部五个 setter 必须返回 ErrBrokerAccountNotFound。
func TestD388_BrokerSetters_NotFound(t *testing.T) {
	s := d388Store(t)
	ctx := context.Background()
	bps := 1.5
	tr := true
	cases := []struct {
		name string
		fn   func() error
	}{
		{"status", func() error { return s.SetBrokerAccountStatus(ctx, "ghost", "stopped") }},
		{"alias", func() error { return s.SetBrokerAccountAlias(ctx, "ghost", "x") }},
		{"website", func() error { return s.SetBrokerWebsite(ctx, "ghost", "https://x") }},
		{"commission", func() error { return s.SetBrokerCommission(ctx, "ghost", &bps) }},
		{"is_real", func() error { return s.SetBrokerAccountIsReal(ctx, "ghost", &tr) }},
	}
	for _, c := range cases {
		if err := c.fn(); !errors.Is(err, ErrBrokerAccountNotFound) {
			t.Errorf("%s: want ErrBrokerAccountNotFound, got %v", c.name, err)
		}
	}
}

// 存在 venue：全部 setter 照常生效（硬化不误伤正常路径）。
func TestD388_BrokerSetters_ExistOK(t *testing.T) {
	s := d388Store(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, status)
		 VALUES ('vX', 1, 'h', 443, 1, 'p', 's', 'connected')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	bps := 1.5
	tr := true
	for name, fn := range map[string]func() error{
		"status":     func() error { return s.SetBrokerAccountStatus(ctx, "vX", "stopped") },
		"alias":      func() error { return s.SetBrokerAccountAlias(ctx, "vX", "x") },
		"website":    func() error { return s.SetBrokerWebsite(ctx, "vX", "https://x") },
		"commission": func() error { return s.SetBrokerCommission(ctx, "vX", &bps) },
		"is_real":    func() error { return s.SetBrokerAccountIsReal(ctx, "vX", &tr) },
	} {
		if err := fn(); err != nil {
			t.Errorf("%s on existing venue: %v", name, err)
		}
	}
}

// BrokerVenueDiscoverable 三分支：stopped/failed→false、connected→true、absent(CEX 自管)→true。
func TestD388_BrokerVenueDiscoverable(t *testing.T) {
	m := map[string]string{"a": "connected", "b": "stopped", "c": "failed", "d": "unknown"}
	for _, tc := range []struct {
		venue string
		want  bool
	}{
		// R0-A3：'unknown' 第四值必须丢弃——判据是 ≠connected 而非枚举
		{"a", true}, {"b", false}, {"c", false}, {"d", false}, {"cex-venue", true},
	} {
		if got := BrokerVenueDiscoverable(m, tc.venue); got != tc.want {
			t.Errorf("venue %s: got %v want %v", tc.venue, got, tc.want)
		}
	}
	// nil map（查询失败 fail-open）全放行
	if !BrokerVenueDiscoverable(nil, "b") {
		t.Error("nil statuses should fail-open")
	}
}
