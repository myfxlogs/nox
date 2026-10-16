package econkb

import (
	"os"
	"path/filepath"
	"testing"
)

func seedDir(t *testing.T) string {
	d := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("hit1.md", "# x\n**判据挂接**：EA-05\nmatch:\n  payoff_class: naked_short_vol\n")
	write("hit2.md", "# y\n**判据挂接**：EA-01\nmatch:\n  canonicals: [XAUUSD, PAXGUSDT]\n  venues: [gate]\n")
	write("nomatch.md", "# z\n**判据挂接**：EA-11\n")
	return d
}

func TestMatchHits(t *testing.T) {
	d := seedDir(t)
	// ①payoff_class 精确命中
	hits := MatchHits(d, Input{PayoffClass: "naked_short_vol"})
	if len(hits) != 1 || hits[0].ID != "hit1" || hits[0].EALink != "EA-05" {
		t.Fatalf("payoff hit: %+v", hits)
	}
	// ②canonical+venue 双键命中（venue_b 腿任一即中）
	hits = MatchHits(d, Input{Canonical: "PAXGUSDT", VenueB: "gate"})
	if len(hits) != 1 || hits[0].ID != "hit2" {
		t.Fatalf("canon+venue hit: %+v", hits)
	}
	// ③venue 不中→零命中（宁漏不误中）
	if hits := MatchHits(d, Input{Canonical: "PAXGUSDT", VenueA: "binance"}); len(hits) != 0 {
		t.Fatalf("venue 不中应零命中: %+v", hits)
	}
	// ④无 match 段条目不参与（canonical 键在非空但输入 venue 缺 → hit2 也不中——
	// 输入 canonical 有键但 venues 键要求 venue 在，VenueB 空 → 不中）
	if hits := MatchHits(d, Input{Canonical: "XAUUSD"}); len(hits) != 0 {
		t.Fatalf("venue 键未供应不中: %+v", hits)
	}
	// ⑤库缺=空集非阻塞
	if hits := MatchHits("/nonexistent", Input{PayoffClass: "x"}); hits != nil {
		t.Fatal("库缺应空集")
	}
}
