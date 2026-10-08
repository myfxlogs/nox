package dashboard

import (
	"context"
	"strings"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- 经验库（§2.9/§13.6） ---

func (s *Service) ListKnowledgeEntries(ctx context.Context, req *connect.Request[v1.ListKnowledgeEntriesRequest]) (*connect.Response[v1.ListKnowledgeEntriesResponse], error) {
	entries, err := s.store.ListKnowledgeEntries(ctx, "", 200, 0)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pb []*v1.KnowledgeEntry
	for i := range entries {
		e := &entries[i]
		pe := &v1.KnowledgeEntry{
			Id:             e.ID,
			Signature:      e.Signature,
			Venue:          strings.Join(e.VenueKeys, ","),
			Symbol:         e.Symbol,
			Verdict:        e.Verdict,
			Rationale:      e.Rationale,
			Source:         e.Source,
			Status:         e.Status,
			ValidationNote: e.ValidationNote,
		}
		if !e.Ts.IsZero() {
			pe.Ts = timestamppb.New(e.Ts)
		}
		if e.ValidatedAt != nil {
			pe.ValidatedAt = timestamppb.New(*e.ValidatedAt)
		}
		pb = append(pb, pe)
	}
	return connect.NewResponse(&v1.ListKnowledgeEntriesResponse{Entries: pb}), nil
}
