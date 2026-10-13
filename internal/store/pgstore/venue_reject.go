package pgstore

import (
	"context"
	"time"
)

// VenueReject fin_venue_reject 行（D-255 venue 败因坑表）。
type VenueReject struct {
	Venue          string
	Op             string
	ErrorSignature string
	Action         string // fix|suppress|critical
	ActionParams   []byte // jsonb
	Hits           int
	FirstSeen      time.Time
	LastSeen       time.Time
	ExpiresAt      *time.Time
}

// Active 抑单窗内（suppress/critical 且未过期）。
func (r *VenueReject) Active(now time.Time) bool {
	if r.Action == "fix" {
		return true // fix 型常驻生效（参数映射）
	}
	return r.ExpiresAt == nil || r.ExpiresAt.After(now)
}

// LearnVenueReject 下单败行自动落坑（PK upsert：新坑 action=suppress+24h 窗——
// 「同一败因二次触发零 venue 打面」默认动作；已存坑 hits+1/last_seen 更新，
// 委员会手改 action/expires 不被覆写）。
func (s *Store) LearnVenueReject(ctx context.Context, venue, op, sig string, window time.Duration) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO fin_venue_reject
		(venue, op, error_signature, action, expires_at)
		VALUES ($1,$2,$3,'suppress',now()+$4*interval '1 second')
		ON CONFLICT (venue, op, error_signature) DO UPDATE SET
			hits=fin_venue_reject.hits+1, last_seen=now()`,
		venue, op, sig, window.Seconds())
	return err
}

// ListVenueRejects 该 venue+op 坑集（tryLeg 申购前查坑读面）。
func (s *Store) ListVenueRejects(ctx context.Context, venue, op string) ([]VenueReject, error) {
	rows, err := s.pool.Query(ctx, `SELECT venue, op, error_signature, action,
		action_params, hits, first_seen, last_seen, expires_at
		FROM fin_venue_reject WHERE venue=$1 AND op=$2`, venue, op)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VenueReject
	for rows.Next() {
		var r VenueReject
		if err := rows.Scan(&r.Venue, &r.Op, &r.ErrorSignature, &r.Action,
			&r.ActionParams, &r.Hits, &r.FirstSeen, &r.LastSeen, &r.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
