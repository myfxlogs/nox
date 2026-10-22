// reconcile_venue_attr_test.go —— 托管池跨 venue 归因锚（STATE 行45）：
// okx DCI 回执行剔出 binance 池 ledger 口径（#41 0.0207 ping-pong 病灶）。
package dciexec

import (
	"testing"

	"nox/internal/store/pgstore"
)

func TestDciPoolVenue_Attribution(t *testing.T) {
	okx, bn := "okx", "binance"
	cases := []struct {
		name string
		pos  pgstore.PersonalPosition
		want string
	}{
		{"列读 okx", pgstore.PersonalPosition{Venue: &okx, Note: "DCI x 申购回执：binance x"}, "okx"}, // 列优先于 note 词（1071 列转正钉）
		{"列空回落 note 词 okx", pgstore.PersonalPosition{Note: "DCI x 申购回执：okx DCD strike"}, "okx"},
		{"note 词 binance", pgstore.PersonalPosition{Venue: &bn, Note: "DCI x 申购回执：binance RWUSDT"}, "binance"},
		{"无列无词=binance 兜底", pgstore.PersonalPosition{Note: "手动池行无 venue 词"}, "binance"},
	}
	for _, c := range cases {
		if got := dciPoolVenue(c.pos); got != c.want {
			t.Fatalf("%s → %s want %s", c.name, got, c.want)
		}
	}
}
