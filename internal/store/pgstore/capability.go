// capability.go —— D-186①：能力账本聚合视图（只读拼读现有表：
// 凭据=exchange_configs/broker_accounts 只记有无+能力标签不落明文；
// 资金桶=fin_venue_account_snapshot 每所最新行；持仓=personal_positions OPEN；
// 约束=fin_capability_constraint；缺口=fin_condition_gap）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// CapabilityLedger 能力账本聚合视图（D-187 匹配引擎消费面）。
type CapabilityLedger struct {
	Credentials []VenueCredentialCap   `json:"credentials"`
	Buckets     []FundBucket           `json:"buckets"`
	Holdings    []CapabilityHolding    `json:"holdings"`
	Constraints []CapabilityConstraint `json:"constraints"`
	Gaps        []ConditionGap         `json:"gaps"`
	Permissions []CapabilityPermission `json:"permissions"`
}

// CapabilityPermission 权限探测行（fin_capability_permission，只读探活结果）。
type CapabilityPermission struct {
	Venue      string `json:"venue"`
	Permission string `json:"permission"`
	State      string `json:"state"`
	ProbedAt   string `json:"probed_at"`
}

// VenueCredentialCap 凭据存在面（零明文：只记 venue+能力标签）。
type VenueCredentialCap struct {
	Venue     string   `json:"venue"`
	VenueType string   `json:"venue_type"`
	Tags      []string `json:"tags"`
}

// FundBucket 资金桶（venue 最新账户快照）。
type FundBucket struct {
	Venue     string  `json:"venue"`
	VenueType string  `json:"venue_type"`
	EquityRaw float64 `json:"equity_raw"`
	Currency  string  `json:"currency"`
	AsOf      string  `json:"as_of"`
}

// CapabilityHolding 个人持仓能力面（kind+qty）。
type CapabilityHolding struct {
	Asset string  `json:"asset"`
	Kind  string  `json:"kind"`
	Qty   float64 `json:"qty"`
}

// CapabilityConstraint 约束行（fin_capability_constraint）。
type CapabilityConstraint struct {
	ID         int64    `json:"id"`
	Scope      string   `json:"scope"`
	PositionID *int64   `json:"position_id,omitempty"`
	Subject    string   `json:"subject"`
	Qualifier  string   `json:"qualifier"`
	ValueNum   *float64 `json:"value_num,omitempty"`
	Note       string   `json:"note"`
	Source     string   `json:"source"`
}

// ConditionGap 条件缺口行（fin_condition_gap）。
type ConditionGap struct {
	ID          int64     `json:"id"`
	TemplateID  string    `json:"template_id"`
	Kind        string    `json:"kind"`
	Subject     string    `json:"subject"`
	Qualifier   string    `json:"qualifier"`
	RequestText string    `json:"request_text"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// ListCapabilityLedger 聚合四源+两新表（任一面缺行=该面空非错）。
func (s *Store) ListCapabilityLedger(ctx context.Context) (*CapabilityLedger, error) {
	l := &CapabilityLedger{}
	crows, err := s.pool.Query(ctx, `SELECT venue, venue_type FROM exchange_configs ORDER BY venue`)
	if err != nil {
		return nil, fmt.Errorf("ledger credentials: %w", err)
	}
	for crows.Next() {
		var c VenueCredentialCap
		if err := crows.Scan(&c.Venue, &c.VenueType); err != nil {
			crows.Close()
			return nil, err
		}
		c.Tags = []string{"cex", "private_api"}
		l.Credentials = append(l.Credentials, c)
	}
	crows.Close()
	brows, err := s.pool.Query(ctx, `SELECT venue, venue_type, status FROM broker_accounts ORDER BY venue`)
	if err != nil {
		return nil, fmt.Errorf("ledger broker credentials: %w", err)
	}
	for brows.Next() {
		var c VenueCredentialCap
		var vt, status string
		if err := brows.Scan(&c.Venue, &vt, &status); err != nil {
			brows.Close()
			return nil, err
		}
		c.VenueType = "cfd"
		c.Tags = []string{"cfd", vt, status}
		l.Credentials = append(l.Credentials, c)
	}
	brows.Close()

	srows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue) venue, venue_type,
		       COALESCE(equity_raw, balance_raw, 0), currency, fetched_at::text
		FROM fin_venue_account_snapshot ORDER BY venue, fetched_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("ledger buckets: %w", err)
	}
	for srows.Next() {
		var b FundBucket
		if err := srows.Scan(&b.Venue, &b.VenueType, &b.EquityRaw, &b.Currency, &b.AsOf); err != nil {
			srows.Close()
			return nil, err
		}
		l.Buckets = append(l.Buckets, b)
	}
	srows.Close()

	hrows, err := s.pool.Query(ctx, `
		SELECT asset, kind, COALESCE(qty, 0) FROM personal_positions
		WHERE status='OPEN' ORDER BY asset, kind`)
	if err != nil {
		return nil, fmt.Errorf("ledger holdings: %w", err)
	}
	for hrows.Next() {
		var h CapabilityHolding
		if err := hrows.Scan(&h.Asset, &h.Kind, &h.Qty); err != nil {
			hrows.Close()
			return nil, err
		}
		l.Holdings = append(l.Holdings, h)
	}
	hrows.Close()

	cons, err := s.ListCapabilityConstraints(ctx)
	if err != nil {
		return nil, err
	}
	l.Constraints = cons
	gaps, err := s.ListConditionGaps(ctx, "")
	if err != nil {
		return nil, err
	}
	l.Gaps = gaps
	prows, err := s.pool.Query(ctx, `
		SELECT venue, permission, state, probed_at::text
		FROM fin_capability_permission ORDER BY venue, permission`)
	if err != nil {
		return nil, fmt.Errorf("ledger permissions: %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var p CapabilityPermission
		if err := prows.Scan(&p.Venue, &p.Permission, &p.State, &p.ProbedAt); err != nil {
			return nil, err
		}
		l.Permissions = append(l.Permissions, p)
	}
	return l, prows.Err()
}

// UpsertCapabilityPermission 探测结果写入（UNIQUE(venue,permission) 幂等刷新 probed_at）。
func (s *Store) UpsertCapabilityPermission(ctx context.Context, venue, permission, state, note string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_capability_permission (venue, permission, state, note)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (venue, permission) DO UPDATE SET
		  state=EXCLUDED.state, note=EXCLUDED.note, probed_at=now()`,
		venue, permission, state, note)
	if err != nil {
		return fmt.Errorf("upsert capability permission: %w", err)
	}
	return nil
}
