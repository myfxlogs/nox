// d555_paper_cex_test.go —— D-555 S1 验收锚（真库 full-step，仿 d410 形态）：
// A1 保底段（enabled 双CEX combo 行→paper 行 structure/type/venue 断言；cfd 轨「跳过双CEX」
// 日志对本构型消失）+ A2 发现段（detector 候选驱动；D-050 无槽全量开）+ mt×cex 回归锚 +
// 0160 migration 生成列断言。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d555Store DSN 库+本票涉及表清空（mirror d410Store）。
func d555Store(t *testing.T) *pgstore.Store {
	t.Helper()
	return d410Store(t)
}

// d555SeedCombo mirror d410SeedCombo（enabled real 域；venue 双段全型）。
func d555SeedCombo(t *testing.T, s *pgstore.Store, canonBuy, venueBuy, canonSell, venueSell string) {
	t.Helper()
	d410SeedCombo(t, s, canonBuy, venueBuy, canonSell, venueSell, false)
}

// d555Cand FUNDING_HEDGE 候选（typed keys，detector 原样形态）。
func d555Cand(canonical, spotKey, perpKey string, funding float64) CexPaperCand {
	return CexPaperCand{Canonical: canonical, SpotVenueKey: spotKey, PerpVenueKey: perpKey, FundingAnn: funding}
}

func d555Opts() PaperCexOpts {
	return PaperCexOpts{ReopenCooldown: 24 * time.Hour} // D-050：AuthorizedMaxActive 退役
}

// TestD555_A1_GuaranteedSlot A1 红→绿锚：enabled 双CEX combo（gate:spot×gate:perp 同
// canonical）+候选在 → 保底槽开 paper 行；structure/venue_type/venue 断言；mt_venue NULL。
func TestD555_A1_GuaranteedSlot(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")
	cands := []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)}
	opened, _, err := StepPaperCexCarry(ctx, s, cands, time.Now(), d555Opts(), nil, nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 || opened[0] != "XAUUSD" {
		t.Fatalf("A1: want opened=[XAUUSD], got %v", opened)
	}
	active, err := s.ListActivePaperSpotCarry(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("A1: active=%d err=%v", len(active), err)
	}
	p := active[0]
	if p.VenueA != "gate" || p.VenueTypeA != "spot" || p.VenueB != "gate" || p.VenueTypeB != "perp" {
		t.Fatalf("A1: venue/type = %s/%s×%s/%s, want gate/spot×gate/perp", p.VenueA, p.VenueTypeA, p.VenueB, p.VenueTypeB)
	}
	if p.Structure != "long_spot_short_perp" || p.Side != "short_perp" {
		t.Fatalf("A1: structure/side = %s/%s, want long_spot_short_perp/short_perp", p.Structure, p.Side)
	}
	if p.MtVenue != nil {
		t.Fatalf("A1: mt_venue want NULL, got %v", *p.MtVenue)
	}
	if p.EntryFundingAnn != 12.5 {
		t.Fatalf("A1: entry_funding_ann = %v, want 12.5", p.EntryFundingAnn)
	}
	// 生成列：venue_key_a/b=typed 键（0160 断言）。
	var ka, kb string
	if err := s.Pool().QueryRow(ctx, `SELECT venue_key_a, venue_key_b FROM paper_spot_carry WHERE id=$1`, p.ID).Scan(&ka, &kb); err != nil {
		t.Fatalf("A1: venue_key read: %v", err)
	}
	if ka != "gate:spot" || kb != "gate:perp" {
		t.Fatalf("A1: venue_key = %s×%s, want gate:spot×gate:perp", ka, kb)
	}
	// 二轮幂等：active 唯一索引兜底不重复开。
	if _, _, err := StepPaperCexCarry(ctx, s, cands, time.Now(), d555Opts(), nil, nil); err != nil {
		t.Fatalf("A1 rerun: %v", err)
	}
	active2, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active2) != 1 {
		t.Fatalf("A1: rerun 后应仍 1 行, got %d", len(active2))
	}
}

// TestD555_A1_GuaranteedNeedsCandidate 保底语义=数据缺席不开：combo 在但候选缺席 → 零行
// （同 mt×cex「universe 未产行」，不臆造 funding）。
func TestD555_A1_GuaranteedNeedsCandidate(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")
	opened, _, err := StepPaperCexCarry(ctx, s, nil, time.Now(), d555Opts(), nil, nil)
	if err != nil || len(opened) != 0 {
		t.Fatalf("A1 缺候选: opened=%v err=%v", opened, err)
	}
}

// TestD555_A2_DiscoverySlot A2 发现段：无 combo，纯 detector 候选驱动——
// D-050 数量槽退役：干净候选全量开（同 canonical 不同 venue 对各行独立开）。
func TestD555_A2_DiscoverySlot(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	cands := []CexPaperCand{
		d555Cand("PONS", "gate:spot", "binance:perp", 70),
		d555Cand("PONS", "okx:spot", "gate:perp", 65), // 同 canonical 第二 venue 对
		d555Cand("BTW", "gate:spot", "okx:perp", 43),
	}
	opened, _, err := StepPaperCexCarry(ctx, s, cands, time.Now(), d555Opts(), nil, nil)
	if err != nil {
		t.Fatalf("A2: %v", err)
	}
	if len(opened) != 3 {
		t.Fatalf("A2: want opened=3（PONS 两 venue 对各行+BTW）, got %v", opened)
	}
	// D-050 无槽：truncate 后同候选集重跑——全量重开（无上限挤出；原 cap 锚随槽退役）。
	if _, err := s.Pool().Exec(ctx, `TRUNCATE paper_spot_carry, carry_graduations RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("A2 truncate: %v", err)
	}
	opened2, _, err := StepPaperCexCarry(ctx, s, cands, time.Now(), d555Opts(), nil, nil)
	if err != nil || len(opened2) != 3 {
		t.Fatalf("A2 无槽全量: opened=%v err=%v, want 3", opened2, err)
	}
}

// TestD555_CfdSlotSilentForSpotPerp A1 日志消失锚+mt×cex 回归锚：
// spot×perp 构型 combo → cfd 轨零动作（不再打「跳过双CEX」；paper 行归 cex 轨产出）；
// mt×cex combo → cfd 轨保底照常（既有语义回归）。
func TestD555_CfdSlotSilentForSpotPerp(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")           // cex 轨构型
	d410SeedCombo(t, s, "XAUUSD", "3001157:mt5", "XAUUSDT", "gate:perp", false) // mt×cex 回归
	// cfd 轨步进：spot×perp 行不产 cfd paper（零 universe 行配合）；mt×cex 行因 universe
	// 缺席记 missing warn（既有语义——非本票改动面）。
	if _, _, err := StepPaperCfdCarry(ctx, s, nil, time.Now(), d410Opts(), nil); err != nil {
		t.Fatalf("cfd step: %v", err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM paper_spot_carry WHERE structure LIKE '%cfd%'`).Scan(&n); err != nil {
		t.Fatalf("count cfd paper: %v", err)
	}
	if n != 0 {
		t.Fatalf("spot×perp combo 不得由 cfd 轨开仓, got %d cfd 行", n)
	}
	// mt×cex 回归：universe 行在 → cfd 轨照常开（StepPaperCfdCarry 既有测试族已锚，
	// 此处仅验 spot×perp combo 未挤占/未污染 cfd 槽判定）。
	rows := []UniverseRow{d374Row("XAUUSD", "3001157", "gate", "long_cfd_short_perp", 10.95, "")}
	d144BookCover(t, s, "XAUUSD", "3001157", "gate") // D-144 ③
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("mt×cex 回归: opened=%v err=%v", opened, err)
	}
}
