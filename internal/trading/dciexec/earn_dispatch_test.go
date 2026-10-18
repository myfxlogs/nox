package dciexec

import (
	"context"
	"strings"
	"testing"

	"nox/internal/store/pgstore"
)

// TestConcentration_StalePoolDispatch D-622 三批：分母池缺 info 末行实施席转发令。
func TestConcentration_StalePoolDispatch(t *testing.T) {
	ex, f, _ := earnFixture()
	_ = f
	ex.concentrationAlert(context.Background(), []pgstore.CapitalPoolRow{
		{Venue: "binance", VenueType: "spot", Equity: 9000, Currency: "USDT", Stale: true},
	})
	var saw bool
	for _, a := range f.alerts {
		if strings.HasSuffix(a, "[角色:实施] 池快照缺口排查（读 STATE 本行——分母池缺 venue:type 清单在告警文）") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("分母池缺末行令缺: %v", f.alerts)
	}
}
