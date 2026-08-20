// symbol_seed_test.go —— D-060 F7/F8：CFD 探测面扩容幂等 seed 工具。
// 仅当 ARB_SYMBOL_SEED=1 且 ARB_DEMO_PG_DSN 设置时运行（env 门控，不进 CI 常跑）。
//
// 流程：本进程内起 BrokerManager（boot 恢复 connected 账号，复用 fetch/订阅/swap 路径）→
// 逐 venue 按方案 §4 F8 候选表 + 变体做大小写不敏感匹配（含 cent 后缀变体）→
// 命中且 TradeMode≠0（禁交易符号不映射）才插 broker_symbol_map（幂等：存在即跳过，
// 天然键 (venue, broker_symbol) 唯一约束）→ 订阅生效三步（顺序敏感，缺一不可——
// ReloadSymbolMap 只刷 symMap 不订阅，D-042 订阅路径独立）：
//  1. ReloadSymbolMap(venue)：先刷 symMap，防早期报价以 broker 原名落 canonical；
//  2. Resubscribe(venue, 新增 broker 符号)：拉起报价流；
//  3. swap 补轮：新符号 InPoints 归一需报价，缺价走 ErrSwapColdStart 退避自愈。
//
// 注意：本工具在本进程内验证链路（映射/订阅/swap 到流）；生产 core 进程经部署重启后
// 由 symbolPlan 读 DB 新映射生效。一个 canonical 每 venue 多命中 → WARN 跳过，不自动猜。
package dashboard

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// seedAliases canonical → 候选名 ∪ 变体（全部大写比较；方案 §4 F8 候选表）。
// Exness cent 后缀（USDJPYm）经 trimCentSuffix 变体命中，不单列。
var seedAliases = map[string][]string{
	"USDJPY": {"USDJPY"},
	"EURJPY": {"EURJPY"},
	"GBPJPY": {"GBPJPY"},
	"AUDUSD": {"AUDUSD"},
	"USDCAD": {"USDCAD"},
	"USDCHF": {"USDCHF"},
	"NZDUSD": {"NZDUSD"},
	"EURGBP": {"EURGBP"},
	"US30":   {"US30", "DJ30"},
	"NAS100": {"NAS100", "US100", "USATECH", "NDX100"},
	"SPX500": {"SPX500", "US500", "USA500", "SP500"},
	"GER40":  {"GER40"},
	"BRENT":  {"BRENT", "UKOIL", "XBRUSD"},
	"WTI":    {"WTI", "XTIUSD"}, // 另补：ICMarkets XTIUSD → 既有 WTI canonical 补第三 venue
}

func TestSymbolSeed(t *testing.T) {
	if os.Getenv("ARB_SYMBOL_SEED") != "1" {
		t.Skip("ARB_SYMBOL_SEED=1 才跑（幂等 seed 工具，手动 gate）")
	}
	dsn := os.Getenv("ARB_DEMO_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_DEMO_PG_DSN 未设置")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer st.Close()

	// 本进程内 BrokerManager：boot 恢复 connected 账号（适配器/订阅/swap 聚合全部复用）。
	qbus := bus.New()
	bm := NewBrokerManager(st, qbus, exec.NewRegistry(), 0)
	bm.StartAll(ctx)

	type seedRow struct {
		venue, sym, canon string
		mode              int32
		cs                float64
	}
	var seeded []seedRow
	newSyms := map[string][]string{} // venue → 新增 broker 符号（Resubscribe 参数）

	accounts, err := st.ListBrokerAccounts(ctx)
	if err != nil {
		t.Fatalf("list broker accounts: %v", err)
	}
	for i := range accounts {
		a := &accounts[i]
		if a.Status != "connected" {
			continue
		}
		groups, err := bm.BrokerSymbols(ctx, a.Venue)
		if err != nil {
			t.Logf("%s: 符号表拉取失败，跳过: %v", a.Venue, err)
			continue
		}
		existing := bm.BrokerSymbolMap(ctx, a.Venue)
		mappedCanon := make(map[string]bool, len(existing))
		for _, c := range existing {
			mappedCanon[c] = true
		}
		bySym := make(map[string]SymbolGroup, len(groups))
		for _, g := range groups {
			bySym[g.Symbol] = g
		}
		for canon, aliases := range seedAliases {
			if mappedCanon[canon] {
				continue // 该 venue 此 canonical 已有映射
			}
			var hits []string
			for _, g := range groups {
				up, upTrim := strings.ToUpper(g.Symbol), strings.ToUpper(trimCentSuffix(g.Symbol))
				for _, al := range aliases {
					if up == al || upTrim == al {
						hits = append(hits, g.Symbol)
						break
					}
				}
			}
			switch {
			case len(hits) == 0:
				continue // 该 venue 无此品种（"—"不强求）
			case len(hits) > 1:
				t.Logf("WARN %s: canonical %s 多命中 %v，跳过待人工定夺（不自动猜）", a.Venue, canon, hits)
				continue
			}
			sym := hits[0]
			g := bySym[sym]
			if g.TradeMode == 0 {
				t.Logf("%s: %s→%s TradeMode=0 禁交易，不映射", a.Venue, sym, canon)
				continue
			}
			if _, dup := existing[sym]; dup {
				continue // 幂等：天然键已存在
			}
			var cs *float64
			if g.ContractSize > 0 {
				v := g.ContractSize
				cs = &v
			}
			if err := st.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
				Venue: a.Venue, BrokerSymbol: sym, Canonical: canon, ContractSize: cs,
			}); err != nil {
				t.Logf("%s: upsert %s→%s: %v", a.Venue, sym, canon, err)
				continue
			}
			seeded = append(seeded, seedRow{a.Venue, sym, canon, g.TradeMode, g.ContractSize})
			newSyms[a.Venue] = append(newSyms[a.Venue], sym)
		}
	}

	// 订阅生效三步（顺序敏感）。
	for venue, syms := range newSyms {
		bm.ReloadSymbolMap(ctx, venue) // ① 先刷 symMap
		bm.Resubscribe(venue, syms)    // ② 拉起报价流
		// ③ swap 补轮：本进程无 Scheduler，直接跑一轮 SwapFacts 等效验证
		//（生产由 SwapKick→scheduler.Kick 完成；新符号缺价走冷启动退避自愈）。
		if fs, err := bm.SwapFacts(ctx); err != nil {
			t.Logf("[%s] swap 补轮: %v（新符号缺价冷启动属预期，退避自愈）", venue, err)
		} else {
			t.Logf("[%s] swap 补轮: %d facts", venue, len(fs))
		}
	}

	// seeded 结果表（施工证据）。
	t.Logf("=== seeded %d 条 ===", len(seeded))
	for _, r := range seeded {
		t.Logf("  %-14s %-10s → %-8s TradeMode=%d CS=%.2f", r.venue, r.sym, r.canon, r.mode, r.cs)
	}

	// 报价流证据：等最多 20s，报告 seeded canonical 的到流情况。
	if len(seeded) > 0 {
		want := make(map[string]bool, len(seeded))
		for _, r := range seeded {
			want[r.canon] = true
		}
		deadline := time.Now().Add(20 * time.Second)
		arrived := map[string]bool{}
		for time.Now().Before(deadline) && len(arrived) < len(want) {
			for _, q := range qbus.AllLatest() {
				if want[q.Symbol] {
					arrived[q.Symbol] = true
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Logf("=== 报价到流 %d/%d canonical ===", len(arrived), len(want))
		for c := range want {
			if !arrived[c] {
				t.Logf("  未到流: %s（订阅生效但缺 tick，部署后由矩阵页复核）", c)
			}
		}
	}
}
