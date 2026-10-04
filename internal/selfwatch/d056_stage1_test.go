// d056_stage1_test.go —— D-056 锚：①组合证据精确配对（同 venue 对双 canonical
// 只计目标；白名单行缺 canonical→「白名单行无品种」fail-closed）；②全报告
// 零票号/内部实现词（D-NNN 正则断言）。
package selfwatch

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"arb/internal/scan"
	"arb/internal/store/pgstore"
)

// TestD056_ComboExactMatch 同 venue 对双 canonical 只计目标：白名单
// (3001157,okx,XAUUSD→XAU,long_cfd_short_perp) 不得混入同窗 PAXG 组合行——
// 净额与证据皆按精确四元组口径。
func TestD056_ComboExactMatch(t *testing.T) {
	ev := goodEval()
	// 同 venue 对的 PAXG 组合（大额 accrued——venue 对口径下会污染净额+天数）。
	ev.Combos = append(ev.Combos, scan.PaperComboStat{
		VenueA: "3001157", VenueB: "okx", Canonical: "PAXG", Structure: "long_cfd_short_perp",
		Rows: 5, ActiveDays: 9.0, AccruedUSD: 99.0,
	})
	// 异结构同名行（venue 对+canonical 同、structure 异）同样不得计入。
	ev.Combos = append(ev.Combos, scan.PaperComboStat{
		VenueA: "3001157", VenueB: "okx", Canonical: "XAU", Structure: "short_cfd_long_perp",
		Rows: 2, ActiveDays: 9.0, AccruedUSD: 50.0,
	})
	rep, err := BuildDaily(context.Background(), testDeps(greenStore(), ev, tNow))
	if err != nil {
		t.Fatal(err)
	}
	body := rep.Body
	if !strings.Contains(body, "目标组合 $3.0000") {
		t.Fatalf("净额应按精确组合口径只计 XAU $3.00（不含 PAXG $99/异结构 $50）:\n%s", body)
	}
	if !strings.Contains(body, "XAU 3001157×okx：6.5d / $3.00") {
		t.Fatalf("证据行应书 canonical+venue 对:\n%s", body)
	}
	if strings.Contains(body, "PAXG") || strings.Contains(body, "$99") || strings.Contains(body, "9.0d") {
		t.Fatalf("非目标组合不得计入证据/净额:\n%s", body)
	}
	if !rep.Stage1OK {
		t.Fatal("目标组合达标应判 Stage 1 就绪")
	}
}

// TestD056_WhitelistMissingCanonical 白名单行缺 canonical → fail-closed：
// 证据与净额两查皆 ❌ 并标「白名单行无品种」。
func TestD056_WhitelistMissingCanonical(t *testing.T) {
	st := greenStore()
	st.allowPairs = []pgstore.XAssetRealCombo{
		stage1Pair,
		{VenueA: "3001157", VenueB: "gate", Canonical: "", Structure: "long_cfd_short_perp"},
	}
	rep, err := BuildDaily(context.Background(), testDeps(st, goodEval(), tNow))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Body, "3001157×gate：白名单行无品种") {
		t.Fatalf("缺 canonical 行应标无品种:\n%s", rep.Body)
	}
	if !strings.Contains(rep.Body, "❌ 窗内净额非负") || !strings.Contains(rep.Body, "❌ Stage 1 组合证据") {
		t.Fatalf("缺品种行应使两查 fail-closed:\n%s", rep.Body)
	}
	if rep.Stage1OK {
		t.Fatal("白名单行无品种不得判就绪")
	}
}

// TestD056_ReportNoTicketIDs 全报告文本零票号/内部实现词。
func TestD056_ReportNoTicketIDs(t *testing.T) {
	rep, err := BuildDaily(context.Background(), testDeps(greenStore(), goodEval(), tNow))
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`D-\d+`).FindString(rep.Body); m != "" {
		t.Fatalf("报告含票号 %q:\n%s", m, rep.Body)
	}
	for _, bad := range []string{"fail-closed", "accrue", "stall", "maker 分级", "cap 已定", "demo", "paper 窗", "窗龄"} {
		if strings.Contains(rep.Body, bad) {
			t.Fatalf("报告含内部词 %q:\n%s", bad, rep.Body)
		}
	}
}
