// d170_leg_attribution_test.go —— D-170：LegActionable 归因文案对拍。
package exec

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// --- D-170：LegActionable 归因文案对拍（fail-closed 行为零动） ---

// d170Store 实现 legActionableStore + marketstate.Deps（钉死 venue 活、session 闭市
// → LegActionable 拒平文案应携 closed 归因而非三因混判）。
type d170Store struct {
	d495FakeStore
	sessions []fact.Fact
}

func (f *d170Store) LatestFactsByKind(_ context.Context, kind string) ([]fact.Fact, error) {
	if kind == fact.KindTradeSession {
		return f.sessions, nil
	}
	return nil, nil
}
func (f *d170Store) FactsSince(context.Context, string, string, string, time.Time) ([]fact.Fact, error) {
	return nil, nil
}
func (f *d170Store) VenueTickLatestAt(context.Context, string) (time.Time, bool, error) {
	return time.Now(), true, nil
}
func (f *d170Store) VenuesByCanonical(context.Context, string) ([]string, error) {
	return []string{"vA"}, nil
}
func (f *d170Store) MacroEventsBetween(context.Context, time.Time, time.Time) ([]pgstore.MacroEvent, error) {
	return nil, nil
}

func TestD170_LegActionableAttribution(t *testing.T) {
	ctx := context.Background()
	// 钉死闭市集：窗 weekday=明天（今天恒不在窗）→ session 判闭，拒平文案应携
	// closed 归因（fail-closed 行为零动）。
	tmrw := (int(time.Now().UTC().Weekday()) + 1) % 7
	st := &d170Store{
		d495FakeStore: d495FakeStore{changed: false, n: 0, canonMap: map[string]string{"vA|XAUSim": "XAU"}},
		sessions:      []fact.Fact{{Venue: "vA", Symbol: "XAU", Value: float64(tmrw * 1e8)}},
	}
	ok, why := LegActionable(ctx, st, &d495FakeProber{mode: 4}, "vA:mt5", "XAUSim", "BUY", false)
	if ok {
		t.Fatal("冻结腿应拒（fail-closed 零动）")
	}
	if !strings.Contains(why, "closed") {
		t.Fatalf("why=%q 应携 closed 归因", why)
	}
}
