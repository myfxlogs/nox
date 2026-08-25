package testutil

import (
	"strings"
	"testing"
)

func TestIsTestDB(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want bool
	}{
		{"生产 DSN（arb）→false", "postgres://arb:arb@localhost:5433/arb?sslmode=disable", false},
		{"arb_test→true", "postgres://arb:arb@localhost:5433/arb_test?sslmode=disable", true},
		{"解析失败→false", "not a valid dsn", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTestDB(tt.dsn); got != tt.want {
				t.Errorf("isTestDB(%q) = %v, want %v", tt.dsn, got, tt.want)
			}
		})
	}
}

func TestTestDSN_UnsetSkips(t *testing.T) {
	t.Setenv("ARB_TESTUTIL_FAKE_DSN", "")
	TestDSN(t, "ARB_TESTUTIL_FAKE_DSN")
	// t.Skipf 内部调 runtime.Goexit，执行到此=未 Skip=失败
	t.Fatal("应 Skip 未 Skip")
}

func TestTestDSN_TestDBPasses(t *testing.T) {
	t.Setenv("ARB_TESTUTIL_FAKE_DSN", "postgres://arb:arb@localhost:5433/arb_test?sslmode=disable")
	dsn := TestDSN(t, "ARB_TESTUTIL_FAKE_DSN")
	if !strings.Contains(dsn, "arb_test") {
		t.Errorf("TestDSN 返回 %q，应含 arb_test", dsn)
	}
}
