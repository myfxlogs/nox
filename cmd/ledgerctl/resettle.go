// cmd/resettle —— D-422 §1.4/§5：pnl_estimated 仓位重结算工具。
//
// 语义：
//   - 选仓：-pos=<id> 单仓；或 -all（note LIKE '%pnl_estimated%' 的全部仓位）。
//   - 校准重试：MT 可校准腿（:mt4/:mt5 + OPEN+FILLED+OrderID）按 broker_accounts
//     临时建 adapter→OrderHistoryQuerier 重查历史；任一腿 history 不可得 →
//     "history unreachable, keep"（不清 note、不动 settlement）——历史不可得不是
//     放行凭据（诚实语义，D-422：不可静默把不可得当成功）。
//   - 全部可校准腿命中（或无可校准腿=纯 CEX 仓公式价即权威）→ 重写 realized_pnl=
//     混合权威值（MT Profit×fx + CEX 公式价）、清 note、skipped→pending、调 SettleAt。
//   - -dry-run：只判定+报告，不写库不调结算。
//   - 幂等可重跑：SettleAt 自身 applied/skipped 短路；清 note 与 stats_status 更新幂等。
//
// 安全闸（§5）：数据库名必须含 _test（解析 DSN path 尾部，不做整体字符串匹配——
// "arb" 出现在用户名/主机不算数）；否则必须显式 -allow-live。首跑目标 pos=498。
//
// 用法：
//
//	ARB_PG_DSN="postgres://arb:arb@host:5432/arb_test?sslmode=disable" go run ./cmd/resettle -pos=498 -dry-run
//	ARB_PG_DSN="postgres://arb:arb@host:5432/arb?sslmode=disable"     go run ./cmd/resettle -pos=498 -allow-live
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"arb/internal/data/keydomain"
	"time"

	"arb/internal/data/adapter"
	"arb/internal/ledger/reconcile"
	"arb/internal/ledger/settlement"
	"arb/internal/portfolio/lifecycle"
	"arb/internal/store/pgstore"
)

func runResettle() {
	fs := flag.NewFlagSet("resettle", flag.ExitOnError)
	posID := fs.Int64("pos", 0, "单仓 position ID（与 -all 二选一）")
	all := fs.Bool("all", false, "处理全部 note LIKE '%pnl_estimated%' 的仓位")
	dry := fs.Bool("dry-run", false, "只判定+报告，不写库不调结算")
	allowLive := fs.Bool("allow-live", false, "显式允许非 _test 库（生产重结算需显式授权）")
	fs.Parse(os.Args[2:])
	if (*posID <= 0) == !*all {
		log.Fatal("-pos 与 -all 二选一（必填其一）")
	}
	dsn := os.Getenv("ARB_PG_DSN") // D-145 引导件：连库前必需
	if dsn == "" {
		log.Fatal("ARB_PG_DSN 未设置")
	}
	dbName := dbNameFromDSN(dsn)
	if err := checkDSNSafety(dsn, *allowLive); err != nil {
		log.Fatalf("拒绝运行：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		log.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()
	deps := &settleDeps{store: s}
	var ids []int64
	if *posID > 0 {
		ids = []int64{*posID}
	} else {
		rows, err := s.Pool().Query(ctx, `SELECT id FROM positions WHERE note LIKE '%pnl_estimated%' ORDER BY id`)
		if err != nil {
			log.Fatalf("list estimated positions: %v", err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				log.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		rows.Close()
	}
	fmt.Printf("=== D-422 resettle：db=%s dry=%v targets=%d ===\n", dbName, *dry, len(ids))
	tool := &resettler{store: s, deps: deps, queriers: map[string]reconcile.OrderHistoryQuerier{}, qerr: map[string]error{}}
	for _, id := range ids {
		tool.run(ctx, id, *dry)
	}
	fmt.Println("DONE")
}

// dbNameFromDSN 解析 DSN path 尾部数据库名（不做整体字符串匹配——"arb" 在用户/主机段不算数）。
func dbNameFromDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

// checkDSNSafety D-422 §5：数据库名含 _test 或显式 -allow-live 才放行（连接前校验，
// 任何写路径之前拦截）。
func checkDSNSafety(dsn string, allowLive bool) error {
	name := dbNameFromDSN(dsn)
	if allowLive || strings.Contains(name, "_test") {
		return nil
	}
	return fmt.Errorf("数据库名 %q 不含 _test（生产重结算须显式 -allow-live）", name)
}

type resettler struct {
	store    *pgstore.Store
	deps     settlement.Deps
	queriers map[string]reconcile.OrderHistoryQuerier // venueBase → querier（懒建）
	qerr     map[string]error                         // venueBase → 建连失败（"history unreachable"）
}

// run 单仓重结算：校准可得才动（清 note+pending+SettleAt），不可得如实报告保留。
func (t *resettler) run(ctx context.Context, posID int64, dry bool) {
	pos, err := t.store.GetPosition(ctx, posID)
	if err != nil || pos == nil {
		fmt.Printf("pos=%d: position not found err=%v\n", posID, err)
		return
	}
	// 处理条件：note 含 pnl_estimated，或结算仍 skipped（resend 幂等路径——
	// 前次已清 note 但 quality 未升级时仍可补升重结算）。
	stats, _ := t.store.GetSettlementStatsStatus(ctx, posID)
	if !strings.Contains(pos.Note, "pnl_estimated") && stats != "skipped" {
		fmt.Printf("pos=%d: no pnl_estimated note & stats=%s, skip\n", posID, stats)
		return
	}
	legs, err := t.store.ListPositionLegs(ctx, posID)
	if err != nil {
		fmt.Printf("pos=%d: list legs err=%v\n", posID, err)
		return
	}
	// 可校准腿 = OPEN+FILLED+OrderID+MT venue（:mt4/:mt5 有历史查证器语义）。
	var calibratable []pgstore.PositionLeg
	for _, l := range legs {
		if l.Phase == "OPEN" && l.Status == "FILLED" && l.OrderID != "" &&
			keydomain.IsMTKey(l.Venue) {
			calibratable = append(calibratable, l)
		}
	}
	quoteCcy := canonicalQuoteCcy(pos.OpportunityID)
	fx := 1.0
	if quoteCcy != "" && quoteCcy != "USD" {
		rate, ok := t.deps.USDPer(ctx, quoteCcy, time.Now())
		if !ok || rate <= 0 {
			fmt.Printf("pos=%d: fx unavailable quote=%s, keep\n", posID, quoteCcy)
			return
		}
		fx = rate
	}
	auth := 0.0
	for _, l := range calibratable {
		q, ok := t.historyQuerier(ctx, l.Venue)
		if !ok {
			fmt.Printf("pos=%d: history unreachable for %s, keep\n", posID, l.Venue)
			return
		}
		h, err := q.QueryOrderHistory(ctx, l.OrderID, 3)
		if err != nil || h == nil {
			fmt.Printf("pos=%d: history unreachable/miss %s:%s err=%v, keep\n", posID, l.Venue, l.OrderID, err)
			return
		}
		auth += h.Profit * fx
	}
	// 非校准腿（CEX 等）：公式价 PnL（与 sumClosedLegsPnL 同口径——价即权威）。
	closeByLeg := map[int16]*pgstore.PositionLeg{}
	for i := range legs {
		if legs[i].Phase == "CLOSE" && legs[i].Status == "FILLED" {
			closeByLeg[legs[i].LegNo] = &legs[i]
		}
	}
	for _, l := range legs {
		if l.Phase != "OPEN" || l.Status != "FILLED" ||
			keydomain.IsMTKey(l.Venue) {
			continue
		}
		cl := closeByLeg[l.LegNo]
		if cl == nil || cl.PriceActual == nil || l.PriceActual == nil {
			continue
		}
		qty := l.EffectiveQty()
		if cl.FilledQty != nil && *cl.FilledQty > 0 {
			qty = *cl.FilledQty
		}
		dir := 1.0
		if l.Side == "SELL" {
			dir = -1.0
		}
		auth += (*cl.PriceActual - *l.PriceActual) * qty * dir * fx
	}
	if dry {
		fmt.Printf("pos=%d: would resettle authPnL=%.4f (calibratable=%d)\n", posID, auth, len(calibratable))
		return
	}
	// 条件清除：校准可得才清 note + 重写 realized_pnl + skipped→pending。
	if _, err := t.store.Pool().Exec(ctx,
		`UPDATE positions SET realized_pnl=$2,
		   note=btrim(regexp_replace(COALESCE(note,''), '\s*;?\s*pnl_estimated:[^;]*', '', 'g'), '')
		 WHERE id=$1`, posID, auth); err != nil {
		fmt.Printf("pos=%d: clear note/update pnl err=%v, keep\n", posID, err)
		return
	}
	// 校准可达证明该仓现金流源自实采（hp/fill 路径）——存量 estimated 行是
	// 误标（writeCloseCashflows 旧版硬编码 estimated），升 'actual' 过 stats 质量门。
	// funding 实收由 income_events 入结算，公式投影行升 actual=错额入账
	// （pos498 实证），kind='funding' 永久排除——本 UPDATE 只再服务 swap/commission 误标场景。
	if _, err := t.store.Pool().Exec(ctx,
		`UPDATE position_cashflows SET quality='actual' WHERE position_id=$1 AND quality='estimated' AND kind != 'funding'`, posID); err != nil {
		fmt.Printf("pos=%d: quality→actual err=%v, keep\n", posID, err)
		return
	}
	if _, err := t.store.Pool().Exec(ctx,
		`UPDATE position_settlements SET stats_status='pending' WHERE position_id=$1 AND stats_status='skipped'`, posID); err != nil {
		fmt.Printf("pos=%d: stats_status→pending err=%v, keep\n", posID, err)
		return
	}
	if err := settlement.SettleAt(ctx, t.deps, posID, lifecycle.EliminatorAdapter, lifecycle.FamilyResolverAdapter, time.Now()); err != nil {
		fmt.Printf("pos=%d: SettleAt err=%v\n", posID, err)
		return
	}
	var completeness, quality string
	var net *float64
	_ = t.store.Pool().QueryRow(ctx,
		`SELECT COALESCE(completeness_status,''), COALESCE(pnl_quality,''), net_realized_usd
		   FROM position_settlements WHERE position_id=$1 ORDER BY settled_at DESC LIMIT 1`, posID).
		Scan(&completeness, &quality, &net)
	netStr := "nil"
	if net != nil {
		netStr = fmt.Sprintf("%.4f", *net)
	}
	fmt.Printf("pos=%d: resettled authPnL=%.4f completeness=%s quality=%s net=%s\n",
		posID, auth, completeness, quality, netStr)
}

// historyQuerier 懒建 venue 历史查证器（broker_accounts → adapter.Connect → OrderHistoryQuerier）。
func (t *resettler) historyQuerier(ctx context.Context, venueKey string) (reconcile.OrderHistoryQuerier, bool) {
	base := keydomain.BareOf(venueKey)
	if q, ok := t.queriers[base]; ok {
		return q, true
	}
	if _, failed := t.qerr[base]; failed {
		return nil, false
	}
	a, err := t.store.GetBrokerAccount(ctx, base)
	if err != nil || a == nil {
		t.qerr[base] = fmt.Errorf("no broker account for %s", base)
		return nil, false
	}
	if a.Platform != 1 {
		t.qerr[base] = fmt.Errorf("MT4 已退役：%s platform=%d 不再支持", base, a.Platform) // D-470
		return nil, false
	}
	var q reconcile.OrderHistoryQuerier
	{
		ad := adapter.NewMT5Adapter(a.Venue, a.Host, a.Server, int32(a.Port), a.User, a.Password)
		if err := ad.Connect(ctx); err != nil {
			t.qerr[base] = err
			return nil, false
		}
		q = reconcile.NewMT5OrderHistoryQuerier(a.Venue, ad.Token(), ad.MT5Client())
	}
	t.queriers[base] = q
	return q, true
}

// canonicalQuoteCcy 从 opportunity_id（PREFIX:SYMBOL:...）取 canonical 再求计价币
// （与 exec canonicalFromOppID + notional.QuoteCcy 同款，工具本地复刻）。
func canonicalQuoteCcy(oppID string) string {
	parts := strings.Split(oppID, ":")
	if len(parts) < 2 {
		return ""
	}
	c := parts[1]
	if len(c) == 6 {
		b, q := c[:3], c[3:]
		if isAlpha(b) && isAlpha(q) {
			return strings.ToUpper(q)
		}
	}
	if strings.HasSuffix(c, "USD") || strings.HasSuffix(c, "USDT") {
		return "USD"
	}
	return ""
}

func isAlpha(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			if r < 'a' || r > 'z' {
				return false
			}
		}
	}
	return len(s) > 0
}
