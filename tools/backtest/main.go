// main.go —— P3 回测引擎 CLI（D-625）：历史簿窗回放。
// 序：①P1 覆盖准入闸（不达=「该窗不可判」如实报+as-of 断档诊断，禁产 PnL）；
// ②参数-PnL 矩阵（报告头自带覆盖证明）；③成本口径=fin_cost_model 实测优先，
// 无实测标「假设口径」。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"nox/internal/data/marketstate"
	"nox/internal/store/pgstore"
)

func main() {
	pair := flag.String("pair", "", "簿对（如 BZUSDT-BRENT）")
	cex := flag.String("cex", "", "CEX 腿（如 gate:perp）")
	mt := flag.String("mt", "", "MT 账户键（如 3001157:mt5）")
	fromStr := flag.String("from", "", "窗起 UTC（2026-10-12T00:00Z）")
	toStr := flag.String("to", "", "窗止 UTC")
	structure := flag.String("structure", "", "成本口径结构键（空=假设口径）")
	gridStr := flag.String("grid", "60,100,140;30,50;24,72", "格点 entry;exit;hold（CSV）")
	flag.Parse()
	if *pair == "" || *cex == "" || *mt == "" || *fromStr == "" || *toStr == "" {
		flag.Usage()
		os.Exit(2)
	}
	from, err := time.Parse("2006-01-02T15:04Z", *fromStr)
	if err != nil {
		log.Fatalf("from 解析: %v", err)
	}
	to, err := time.Parse("2006-01-02T15:04Z", *toStr)
	if err != nil {
		log.Fatalf("to 解析: %v", err)
	}
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		log.Fatalf("DB_DSN 未设置")
	}
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer st.Close()
	if err := run(ctx, st, runCfg{
		pair: *pair, cex: *cex, mt: *mt, from: from, to: to,
		structure: *structure, grid: parseGrid(*gridStr),
	}); err != nil {
		log.Fatalf("%v", err)
	}
}

type runCfg struct {
	pair, cex, mt string
	from, to      time.Time
	structure     string
	grid          []Params
}

const cadence = 5 * time.Minute // 簿采集节拍（P1 断供口径同源）

func run(ctx context.Context, st *pgstore.Store, cfg runCfg) error {
	rows, err := loadRows(ctx, st, cfg)
	if err != nil {
		return err
	}
	stats := WindowStatsFrom(rows, cadence)
	sess := marketstate.LoadSessionTable(ctx, st)
	mtCanon := cfg.pair[strings.LastIndex(cfg.pair, "-")+1:]
	ok, reasons := Admit(stats, sessionAdapter{sess}, keyBare(cfg.mt), mtCanon, cfg.to.Sub(cfg.from), cadence)

	fmt.Printf("== 回放窗覆盖证明 ==\n窗 %s → %s｜行数 %d｜最大断档 %s｜断档越阈 %d 次｜noise 缺计 %d",
		cfg.from.Format("01-02 15:04Z"), cfg.to.Format("01-02 15:04Z"),
		stats.Rows, stats.MaxGap.Round(time.Minute), stats.GapBreaches, stats.NoiseNulls)
	if stats.SessionKnown {
		fmt.Printf("｜会话对齐率 %.0f%%", stats.CoverageRate*100)
	}
	fmt.Println()
	if !ok {
		fmt.Println("== 该窗不可判（禁产 PnL——P1 准入闸守）==")
		for _, r := range reasons {
			fmt.Println("× " + r)
		}
		diagnose(ctx, st, rows, stats, cfg)
		return nil
	}

	costLabel, costNote := costBasis(ctx, st, cfg)
	fmt.Printf("== 成本口径：%s%s ==\n", costLabel, costNote)
	mtx := Matrix(rows, cfg.grid)
	fmt.Println("entry_bps\texit_bps\thold_h\t笔数\t胜\t合计bps\t均持仓h")
	for _, m := range mtx {
		fmt.Printf("%.0f\t%.0f\t%.0f\t%d\t%d\t%.1f\t%.1f\n",
			m.EntryBps, m.ExitBps, m.HoldHours, m.Trades, m.Wins, m.TotalBps, m.AvgHoldH)
	}
	return nil
}

// diagnose 不可判诊断（as-of-t 层消费面）：最大断档中点的双腿最新 tick 回溯。
func diagnose(ctx context.Context, st *pgstore.Store, rows []BookRow, stats WindowStats, cfg runCfg) {
	if stats.MaxGap == 0 || stats.Rows < 2 {
		return
	}
	// 最大断档定位（与 WindowStatsFrom 同一遍序）。
	var gapMid time.Time
	for i := 1; i < len(rows); i++ {
		if rows[i].TS.Sub(rows[i-1].TS) == stats.MaxGap {
			gapMid = rows[i-1].TS.Add(stats.MaxGap / 2)
		}
	}
	if gapMid.IsZero() {
		return
	}
	mtCanon := cfg.pair[strings.LastIndex(cfg.pair, "-")+1:]
	for _, leg := range []struct{ name, venue, sym string }{
		{"CEX", keyBare(cfg.cex), cexSym(cfg)},
		{"MT", keyBare(cfg.mt), mtCanon},
	} {
		_, _, ts, ok, err := st.LatestTickAsOf(ctx, leg.venue, leg.sym, gapMid)
		if err != nil {
			log.Printf("诊断读败 %s: %v", leg.name, err)
			continue
		}
		if !ok {
			fmt.Printf("诊断@%s：%s 腿 %s as-of 前零 tick（供源从未建立）\n", gapMid.Format("01-02 15:04Z"), leg.name, leg.sym)
			continue
		}
		fmt.Printf("诊断@%s：%s 腿 %s 最后 tick=%s（距断档中点 %s）\n",
			gapMid.Format("01-02 15:04Z"), leg.name, leg.sym, ts.Format("01-02 15:04Z"), gapMid.Sub(ts).Round(time.Minute))
	}
}

// costBasis 成本口径：fin_cost_model 实测行（fills>0）优先，无实测=假设口径如实标。
func costBasis(ctx context.Context, st *pgstore.Store, cfg runCfg) (label, note string) {
	if cfg.structure == "" {
		return "假设口径", "（-structure 未给——禁冒实测）"
	}
	var fills int
	var fee, drift, fundingGap float64
	err := st.Pool().QueryRow(ctx, `
		SELECT COALESCE(SUM(fills),0), COALESCE(SUM(fee_usd),0), COALESCE(SUM(drift_bps),0),
		       COALESCE(SUM(funding_act-funding_exp),0)
		FROM fin_cost_model WHERE venue=$1 AND structure=$2 AND fills>0`,
		keyBare(cfg.mt), cfg.structure).Scan(&fills, &fee, &drift, &fundingGap)
	if err != nil || fills == 0 {
		return "假设口径", "（fin_cost_model 无实测行——P0 实收写径续件后转实测）"
	}
	return "实测口径", fmt.Sprintf("（cost_model fills=%d fee=$%.2f drift=%.1fbps funding差=%.1f）", fills, fee, drift, fundingGap)
}

func loadRows(ctx context.Context, st *pgstore.Store, cfg runCfg) ([]BookRow, error) {
	rws, err := st.Pool().Query(ctx, `
		SELECT ts, entry_spread_bps, exit_spread_bps, entry_net_bps, funding_annual_pct, noise_bps
		FROM fin_xasset_monitor
		WHERE pair=$1 AND cex_venue=$2 AND mt_venue=$3 AND ts >= $4 AND ts <= $5
		ORDER BY ts`, cfg.pair, cfg.cex, cfg.mt, cfg.from, cfg.to)
	if err != nil {
		return nil, fmt.Errorf("簿窗读: %w", err)
	}
	defer rws.Close()
	var rows []BookRow
	for rws.Next() {
		var r BookRow
		if err := rws.Scan(&r.TS, &r.EntrySpreadBps, &r.ExitSpreadBps, &r.EntryNetBps, &r.FundingAnnualPct, &r.NoiseBps); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, rws.Err()
}

// sessionAdapter marketstate.Table → 准入闸 SessionSource（known 语义直通）。
type sessionAdapter struct{ t marketstate.Table }

func (a sessionAdapter) OpenMinutes(venue, symbol string, from, to time.Time) (int, bool) {
	return a.t.OpenMinutes(venue, symbol, from, to)
}

func keyBare(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i]
		}
	}
	return key
}

func cexSym(cfg runCfg) string {
	// CEX 腿 tick 键=universe symbol 域——簿行 sym 未存，诊断用 funding facts 键域
	// 近似（pair 头段）；如实在缺=零 tick 诊断行，不臆造。
	parts := strings.SplitN(cfg.pair, "-", 2)
	if len(parts) == 2 {
		return parts[0]
	}
	return cfg.pair
}

// parseGrid "60,100;30,50;24,72" → entry×exit×hold 格点积。
func parseGrid(s string) []Params {
	parts := strings.Split(s, ";")
	if len(parts) != 3 {
		log.Fatalf("grid 形=entry;exit;hold CSV: %q", s)
	}
	es, xs, hs := csvFloats(parts[0]), csvFloats(parts[1]), csvFloats(parts[2])
	var out []Params
	for _, e := range es {
		for _, x := range xs {
			for _, h := range hs {
				out = append(out, Params{EntryBps: e, ExitBps: x, HoldHours: h})
			}
		}
	}
	return out
}

func csvFloats(s string) []float64 {
	var out []float64
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil {
			log.Fatalf("格点值 %q: %v", f, err)
		}
		out = append(out, v)
	}
	return out
}
