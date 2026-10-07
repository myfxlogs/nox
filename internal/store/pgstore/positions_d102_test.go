package pgstore

import (
	"context"
	"testing"
)

// TestCountLiveOpenPositions D-102 SQL 锚：真金在仓计数=environment='live' 且
// OPEN/OPENING/LEGGED/CLOSING 任一；demo/legacy_demo/空环境不计，闭合态（CLOSED/
// FAILED）不计。
func TestCountLiveOpenPositions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	rows := []struct{ env, status string }{
		{"live", "OPEN"}, {"live", "OPENING"}, {"live", "LEGGED"}, {"live", "CLOSING"},
		{"live", "CLOSED"}, {"live", "FAILED"},
		{"demo", "OPEN"}, {"legacy_demo", "OPEN"}, {"", "OPEN"},
	}
	for i, r := range rows {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO positions (opportunity_id, type, status, environment, opened_at) VALUES ($1, 'SWAPARB', $2, $3, now())`,
			"D102CNT:SYM:vA:vB:"+string(rune('A'+i)), r.status, r.env); err != nil {
			t.Fatalf("insert %+v: %v", r, err)
		}
	}
	n, err := s.CountLiveOpenPositions(ctx)
	if err != nil {
		t.Fatalf("CountLiveOpenPositions: %v", err)
	}
	if n != 4 {
		t.Fatalf("want 4（live OPEN/OPENING/LEGGED/CLOSING）, got %d", n)
	}
}
