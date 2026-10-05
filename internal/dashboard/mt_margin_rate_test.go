// mt_margin_rate_test.go —— D-006 S1b：effMTMarginRate 四态表驱动锚。
package dashboard

import (
	"errors"
	"testing"
	"time"

	"arb/internal/fact"
)

// TestEffMTMarginRate_S1b：实测新鲜→实测值；实测<下限→抬下限仍实测；缺行/超窗/读败→(0.05,未实测)。
func TestEffMTMarginRate_S1b(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name         string
		fs           []fact.Fact
		err          error
		wantRate     float64
		wantMeasured bool
	}{
		{"fresh measured", []fact.Fact{{Value: 0.2, Ts: now}}, nil, 0.2, true},
		{"fresh below floor clamp", []fact.Fact{{Value: 0.002, Ts: now}}, nil, 0.05, true}, // 500x→20x 档
		{"fresh zero sentinel clamp", []fact.Fact{{Value: 0, Ts: now}}, nil, 0.05, true},   // 无限杠杆哨兵 0=实测抬下限
		{"missing row", nil, nil, 0.05, false},
		{"stale 25h within 72h window", []fact.Fact{{Value: 0.2, Ts: now.Add(-25 * time.Hour)}}, nil, 0.2, true}, // D-068：窗 24h→72h
		{"stale >72h", []fact.Fact{{Value: 0.2, Ts: now.Add(-73 * time.Hour)}}, nil, 0.05, false},
		{"read err", nil, errors.New("db down"), 0.05, false},
	}
	for _, c := range cases {
		rate, measured := effMTMarginRate(c.fs, c.err, now)
		if rate != c.wantRate || measured != c.wantMeasured {
			t.Errorf("%s: got (%v,%v) want (%v,%v)", c.name, rate, measured, c.wantRate, c.wantMeasured)
		}
	}
}
