package hearing

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

type fakeStore struct{ rows []pgstore.ElimHearing }

func (f *fakeStore) InsertElimHearing(_ context.Context, h pgstore.ElimHearing) error {
	f.rows = append(f.rows, h)
	return nil
}

type fakeRunner struct {
	prompts []string
}

func (f *fakeRunner) run(_ context.Context, _ *analyst.Client, system string,
	_ []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
	f.prompts = append(f.prompts, system)
	cb.OnDelta(strings.Repeat("辩", 500))
	return analyst.LoopResult{}, nil
}

func testDeps(fs *fakeStore, fr *fakeRunner) *Deps {
	return &Deps{
		Store: fs, Run: fr.run, Now: time.Now,
		ResolveClient: func(context.Context) (*analyst.Client, error) {
			return &analyst.Client{APIKey: "k"}, nil
		},
	}
}

// TestD249_Debate 钦定锚：模拟淘汰事件→听证行落库（多空双稿≤400字+统计面+理由）。
func TestD249_Debate(t *testing.T) {
	fs := &fakeStore{}
	fr := &fakeRunner{}
	d := testDeps(fs, fr)
	d.Debate(context.Background(), "CARRY", []string{"CARRY", "SWAPARB"},
		Stats{TotalTrades: 31, WinRate: 0.2, AvgPnl: -1.5, MedianPnl: -2.0}, "test: avg<0")
	if len(fs.rows) != 1 {
		t.Fatalf("听证行应落一: %d", len(fs.rows))
	}
	h := fs.rows[0]
	if h.Status != "debated" || h.Strategy != "CARRY" || len(h.Family) != 2 {
		t.Fatalf("行错: %+v", h)
	}
	if !strings.Contains(string(h.Stats), `"total_trades":31`) || h.Reason == "" {
		t.Fatalf("统计面/理由缺: %s %q", h.Stats, h.Reason)
	}
	if n := len([]rune(h.BullText)); n != 400 {
		t.Fatalf("bull 应截 400 字: %d", n)
	}
	if len(fr.prompts) != 2 || fr.prompts[0] == fr.prompts[1] {
		t.Fatalf("应两路不同 prompt: %v", fr.prompts)
	}
}

// TestD249_NoAI AI 未配置=听证缺如正常淘汰——行落 skipped_no_ai 零阻塞。
func TestD249_NoAI(t *testing.T) {
	fs := &fakeStore{}
	d := testDeps(fs, &fakeRunner{})
	d.ResolveClient = func(context.Context) (*analyst.Client, error) { return nil, nil }
	d.Debate(context.Background(), "CARRY", nil, Stats{}, "r")
	if len(fs.rows) != 1 || fs.rows[0].Status != "skipped_no_ai" {
		t.Fatalf("AI 缺配应 skipped_no_ai: %+v", fs.rows)
	}
}
