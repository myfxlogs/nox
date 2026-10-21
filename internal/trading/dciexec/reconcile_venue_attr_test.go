// reconcile_venue_attr_test.go —— 托管池跨 venue 归因锚（STATE 行45）：
// okx DCI 回执行剔出 binance 池 ledger 口径（#41 0.0207 ping-pong 病灶）。
package dciexec

import "testing"

func TestDciPoolVenue_Attribution(t *testing.T) {
	cases := []struct {
		note string
		want string
	}{
		{"DCI x 申购回执：okx DCD strike 95000 tenor 7d qty 0.0207 BTC（单号 a）", "okx"},
		{"DCI x 申购回执：binance RWUSDT strike 8 tenor 30d qty 0.01 BTC（单号 b）", "binance"},
		{"手动池行无 venue 词（存量向后兼容=binance 域）", "binance"},
	}
	for _, c := range cases {
		if got := dciPoolVenue(c.note); got != c.want {
			t.Fatalf("note=%q → %s want %s", c.note, got, c.want)
		}
	}
}
