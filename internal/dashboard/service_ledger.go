package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- 台账（§2.8） ---

func (s *Service) AddLedgerEntry(ctx context.Context, req *connect.Request[v1.AddLedgerEntryRequest]) (*connect.Response[v1.AddLedgerEntryResponse], error) {
	m := req.Msg
	date := time.Now()
	if m.Date != nil {
		date = m.Date.AsTime()
	}
	id, err := s.store.AddLedgerEntry(ctx, &pgstore.LedgerEntry{
		Date:     date,
		Channel:  m.Channel,
		Currency: m.Currency,
		Amount:   m.Amount,
		FeeRate:  m.FeeRate,
		Tier:     m.Tier,
		Note:     m.Note,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AddLedgerEntryResponse{Id: id}), nil
}

func (s *Service) ListLedgerEntries(ctx context.Context, req *connect.Request[v1.ListLedgerEntriesRequest]) (*connect.Response[v1.ListLedgerEntriesResponse], error) {
	entries, err := s.store.ListLedgerEntries(ctx, int(req.Msg.GetLimit()), int(req.Msg.GetOffset()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pb []*v1.LedgerEntry
	for i := range entries {
		e := &entries[i]
		pb = append(pb, &v1.LedgerEntry{
			Id:       e.ID,
			Date:     timestamppb.New(e.Date),
			Channel:  e.Channel,
			Currency: e.Currency,
			Amount:   e.Amount,
			FeeRate:  e.FeeRate,
			Tier:     e.Tier,
			Note:     e.Note,
		})
	}
	return connect.NewResponse(&v1.ListLedgerEntriesResponse{Entries: pb}), nil
}

func (s *Service) LedgerSummary(ctx context.Context, req *connect.Request[v1.LedgerSummaryRequest]) (*connect.Response[v1.LedgerSummaryResponse], error) {
	summaries, err := s.store.LedgerSummary(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pb []*v1.TierSummary
	for i := range summaries {
		t := &summaries[i]
		pb = append(pb, &v1.TierSummary{
			Tier:       t.Tier,
			Inflow:     t.Inflow,
			Outflow:    t.Outflow,
			Net:        t.Net,
			EntryCount: int32(t.EntryCount),
		})
	}
	return connect.NewResponse(&v1.LedgerSummaryResponse{Items: pb}), nil
}

// --- 记账（财务线工作项 2：结构化投资记录，决定/教训/观察） ---

// AddInvestmentRecord 新增一条投资记录。kind 非法 / content 空 → 拒绝（后端校验，D-043 前端只传值）。
func (s *Service) AddInvestmentRecord(ctx context.Context, req *connect.Request[v1.AddInvestmentRecordRequest]) (*connect.Response[v1.AddInvestmentRecordResponse], error) {
	m := req.Msg
	kind := m.GetKind()
	if !validRecordKind(kind) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid kind %q: must be decision/lesson/observation", kind))
	}
	if strings.TrimSpace(m.GetContent()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("content required"))
	}
	id, err := s.store.AddInvestmentRecord(ctx, kind, m.GetAsset(), m.GetContent())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AddInvestmentRecordResponse{Id: id}), nil
}

// ListInvestmentRecords 按 kind/asset/内容子串筛选返回（D-043：筛选/搜索全部后端算）。
func (s *Service) ListInvestmentRecords(ctx context.Context, req *connect.Request[v1.ListInvestmentRecordsRequest]) (*connect.Response[v1.ListInvestmentRecordsResponse], error) {
	m := req.Msg
	records, err := s.store.ListInvestmentRecords(ctx, m.GetKind(), m.GetAsset(), m.GetQ(), int(m.GetLimit()), int(m.GetOffset()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pb []*v1.InvestmentRecord
	for i := range records {
		r := &records[i]
		pb = append(pb, &v1.InvestmentRecord{
			Id:      r.ID,
			Kind:    r.Kind,
			Asset:   r.Asset,
			Content: r.Content,
			Ts:      timestamppb.New(r.Ts),
		})
	}
	return connect.NewResponse(&v1.ListInvestmentRecordsResponse{Items: pb}), nil
}

// validRecordKind 判断 kind 是否合法三态之一。
func validRecordKind(kind string) bool {
	switch kind {
	case "decision", "lesson", "observation":
		return true
	}
	return false
}

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
			Venue:          e.Venue,
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
