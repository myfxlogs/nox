package pgstore

import (
	"context"
	"testing"
)

// 待授权面分层锚：审计行命中提案→go 痕集返回真；无审计行提案=false。
func TestProposalGoAttempted(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO alerts (level, message) VALUES ('warn','提案执行审计 #777001 reject goProposal 条件缺口（耗时 12ms）')`); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM alerts WHERE message LIKE '提案执行审计 #777001%'`)
	got, err := s.ProposalGoAttempted(ctx, []int64{777001, 777002})
	if err != nil {
		t.Fatal(err)
	}
	if !got[777001] || got[777002] {
		t.Fatalf("审计痕联查: %v", got)
	}
}
