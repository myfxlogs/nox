package pgstore

import (
	"context"
	"testing"
)

// TestCredTypeTrueValue 归一病灶锚（请求箱 #6 根因）：broker 凭据行
// venue_type 须注册真值 mt5（原归一 "cfd" 致 type:mt5 键永不命中）。
func TestCredTypeTrueValue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE broker_accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO broker_accounts (venue, venue_type, host, port, password, status) VALUES ('90001','mt5','h',0,'p','connected')`); err != nil {
		t.Fatalf("seed broker: %v", err)
	}
	l, err := s.ListCapabilityLedger(ctx)
	if err != nil {
		t.Fatalf("ListCapabilityLedger: %v", err)
	}
	var found *VenueCredentialCap
	for i := range l.Credentials {
		if l.Credentials[i].Venue == "90001" {
			found = &l.Credentials[i]
		}
	}
	if found == nil {
		t.Fatal("broker cred 行缺席")
	}
	if found.VenueType != "mt5" {
		t.Fatalf("VenueType=%q, want mt5 真值（归一 cfd 是病灶）", found.VenueType)
	}
	if len(found.Tags) < 1 || found.Tags[0] != "cfd" {
		t.Fatalf("Tags=%v, want asset_class=cfd 语义经 tag 保留", found.Tags)
	}
}
