package main

import (
	"strings"
	"testing"
)

// TestFundingEventRouting_legless D-369 S1：leg=无 事件不入实时推送行、计入 digest 汇总；
// leg=XAUUSD（有腿）事件照旧实时。
func TestFundingEventRouting_legless(t *testing.T) {
	leglessEv := fundingEvent{row: normRow{Venue: "gate", HedgeCanonical: "", Row: Row{Symbol: "MYX"}}, kind: fundingNewEntry}
	leggedEv := fundingEvent{row: normRow{Venue: "binance", HedgeCanonical: "XAUUSDT", Row: Row{Symbol: "XAUUSD"}}, kind: fundingNewEntry}
	evs := []fundingEvent{leglessEv, leggedEv}

	var pushLines []string
	var digestSyms []string
	for _, ev := range evs {
		if ev.row.HedgeCanonical == "" {
			digestSyms = append(digestSyms, ev.row.Symbol)
			continue
		}
		pushLines = append(pushLines, fundingEventLine(ev))
	}
	if len(pushLines) != 1 || !strings.Contains(pushLines[0], "XAUUSD") || !strings.Contains(pushLines[0], "leg=XAUUSDT") {
		t.Errorf("有腿事件应照旧实时推送, got %v", pushLines)
	}
	if len(digestSyms) != 1 || digestSyms[0] != "MYX" {
		t.Errorf("leg=无 事件应计入 digest 汇总, got %v", digestSyms)
	}
	// 汇总行格式（每轮至多一条）
	summary := "1 条 leg=无 funding band 事件已压 digest（MYX）"
	if !strings.Contains(summary, "1 条 leg=无 funding band 事件已压 digest") {
		t.Errorf("汇总行格式漂移: %q", summary)
	}
}
