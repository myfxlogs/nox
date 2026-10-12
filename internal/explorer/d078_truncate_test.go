// d078_truncate_test.go —— D-078 验收锚：批超上限由「整批拒」改截断留前 10。
// 11 条→10 落 1 drop；10 条边界不变；探针提案同上限同截断；RunDaily 端到端
// 截断条数并入 res.Drop（计数保真）；未闭合 fenced 真截断仍 fail-closed（d513 锚）。
package explorer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"arb/internal/analyst"

	"arb/internal/store/pgstore"
)

// nProposalBatch 造含 n 条合法提案的 ```explorer-proposals 块（逐行异 canonical
// CAN00..，跑行级校验时配同集 known/fakeStore dispersion）。
func nProposalBatch(n int) string {
	items := make([]string, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, fmt.Sprintf(`{"canonical":"CAN%02d","best_pair":"binance×gate","direction":"w","rationale":"r","anchor":"term_dispersion"}`, i))
	}
	return "```explorer-proposals\n{\"proposals\":[" + strings.Join(items, ",") + "]}\n```\n"
}

// TestD078_ProbeBatchTruncate 探针提案同硬上限同截断（D-048 上限语义随 D-078 调整）。
func TestD078_ProbeBatchTruncate(t *testing.T) {
	items := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		items = append(items, `{"purpose":"p","venue_key":"3001:mt5","canonical":"ETHUSDT","side":"BUY",`+
			`"qty":0.5,"notional_usd":300,"expected_cost_usd":1.2,"auto_close_at":"2999-01-01T00:00:00Z","basis":"x"}`)
	}
	b, trunc, ok := parseBatch("```explorer-proposals\n{\"probe_proposals\":[" + strings.Join(items, ",") + "]}\n```\n")
	if !ok || len(b.GetProbeProposals()) != maxBatchProposals || trunc != 2 {
		t.Fatalf("probe 12→10: ok=%v n=%d trunc=%d want 10/2", ok, len(b.GetProbeProposals()), trunc)
	}
}

// TestD078_RunDailyTruncDrop 端到端：11 条合法提案→10 落库 + res.Drop=1（截断并入，
// 双模型 11 条全废病灶闭环：llm=ok parse=ok inserted=10 drop=1）。
func TestD078_RunDailyTruncDrop(t *testing.T) {
	fs := newFakeStore()
	for i := 0; i < 11; i++ { // 已知集给足 11 个 canonical——前 10 落库全合法
		fs.disp = append(fs.disp, pgstore.ExplorerDispersion{Kind: "funding", Canonical: fmt.Sprintf("CAN%02d", i),
			Venues: 3, Min: 0.01, Max: 0.03, Range: 0.02, Mean: 0.02})
	}
	dir := t.TempDir()
	deps := Deps{Store: fs, Client: analyst.NewClient("http://x", "k", "m"),
		Run: fakeRun(nProposalBatch(11)), Now: func() time.Time {
			return time.Date(2026, 10, 7, 6, 47, 0, 0, BJTime)
		}, InboxPath: dir + "/inbox.md", ResearchDir: dir + "/res"}
	res, err := RunDaily(context.Background(), deps)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if res.Proposals != 10 || res.Drop != 1 || len(fs.proposals) != 10 {
		t.Fatalf("11 条批: proposals=%d drop=%d rows=%d want 10/1/10", res.Proposals, res.Drop, len(fs.proposals))
	}
	// 10 条边界端到端：换新日重跑全落零 drop（无截断计数混入）。
	deps.Now = func() time.Time { return time.Date(2026, 10, 8, 6, 47, 0, 0, BJTime) }
	deps.Run = fakeRun(nProposalBatch(10))
	res2, err := RunDaily(context.Background(), deps)
	if err != nil || res2.Proposals != 10 || res2.Drop != 0 || len(fs.proposals) != 20 {
		t.Fatalf("10 条边界: proposals=%d drop=%d rows=%d err=%v want 10/0/20", res2.Proposals, res2.Drop, len(fs.proposals), err)
	}
}
