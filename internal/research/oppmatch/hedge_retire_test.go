package oppmatch

import (
	"strings"
	"testing"

	"nox/internal/store/pgstore"
)

// TestHedgeScan 对冲腿可行面锚：XAUUSD 裸腿 →asset_equiv GOLD 桥→binance
// 现货 PAXGUSDT+perp XAUUSDT 两腿 hedged 假设产出（net carry 口径对拍）；
// NAS 无桥行 → 「对冲腿：无」注记幂等；rejected 假设不扫。
func TestHedgeScan(t *testing.T) {
	st, ctx := d514Store(t)
	for _, q := range []string{
		`DELETE FROM fin_opp_hypothesis WHERE category_key LIKE 'cfd_swap|%d514%' OR category_key LIKE 'cfd_swap_hedged|%'`,
		`DELETE FROM asset_equiv WHERE venue LIKE 'd514%'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('d514m1','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING`,
		`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical) VALUES ('d514m1','XAUUSDm','XAUUSD') ON CONFLICT DO NOTHING`,
		`INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
		 ('GOLD','XAUUSD','mt5','d514m1'),('GOLD','XAUUSDT','perp','binance'),('GOLD','PAXGUSDT','spot','binance')
		 ON CONFLICT DO NOTHING`,
		`INSERT INTO fin_cex_universe (venue, symbol, base, status, contract_type, last_funding_ann, last_seen)
		 VALUES ('binance','PAXGUSDT','PAXG','TRADING','',0,now()),
		        ('binance','XAUUSDT','XAU','TRADING','PERP',2.0,now())
		 ON CONFLICT (venue, symbol) DO UPDATE SET status='TRADING', base=EXCLUDED.base,
		   contract_type=EXCLUDED.contract_type, last_funding_ann=EXCLUDED.last_funding_ann, last_seen=now()`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts) VALUES
		 ('swap_short','d514m1','XAUUSD',3.7,'bps_daily',now())`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, q := range []string{
			`DELETE FROM asset_equiv WHERE venue LIKE 'd514%' OR (asset_class='GOLD' AND venue='binance' AND canonical IN ('XAUUSDT','PAXGUSDT'))`,
			`DELETE FROM broker_symbol_map WHERE venue='d514m1'`,
			`DELETE FROM fin_cex_universe WHERE venue='binance' AND symbol IN ('PAXGUSDT','XAUUSDT')`,
			`DELETE FROM facts WHERE venue='d514m1'`,
			`DELETE FROM fin_opp_hypothesis WHERE category_key LIKE 'cfd_swap%d514%'`,
		} {
			_, _ = st.Pool().Exec(ctx, q)
		}
	}()
	// 两条裸腿假设：XAUUSD（有腿）+NAS（无桥→无腿注记）。
	for _, k := range []string{"cfd_swap|d514m1|XAUUSD|short", "cfd_swap|d514m1|NAS|long"} {
		parts := strings.Split(k, "|")
		if _, _, _, err := st.UpsertHypothesis(ctx, pgstore.OppHypothesis{
			Venue: parts[1], CategoryKey: k, InstType: "cfd", Shape: "swap_carry",
			Sample: "x", Detail: "裸"}); err != nil {
			t.Fatal(err)
		}
	}
	hedgeScan(ctx, st)
	var hedged int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_opp_hypothesis WHERE category_key LIKE 'cfd_swap_hedged|d514m1|XAUUSD|%'`).
		Scan(&hedged); err != nil {
		t.Fatal(err)
	}
	if hedged != 2 {
		t.Fatalf("现货+perp 两腿应产 2 条 hedged 假设: %d", hedged)
	}
	var det string
	if err := st.Pool().QueryRow(ctx,
		`SELECT detail FROM fin_opp_hypothesis WHERE category_key='cfd_swap_hedged|d514m1|XAUUSD|short|binance|XAUUSDT'`).
		Scan(&det); err != nil {
		t.Fatal(err)
	}
	// net≈3.7×365/100=13.5% − funding 2.0 → ≈11.5。
	if !strings.Contains(det, "13.5%") || !strings.Contains(det, "2.0%") || !strings.Contains(det, "11.5%") {
		t.Fatalf("净 carry 口径错: %s", det)
	}
	var nasDet string
	if err := st.Pool().QueryRow(ctx,
		`SELECT detail FROM fin_opp_hypothesis WHERE category_key='cfd_swap|d514m1|NAS|long'`).
		Scan(&nasDet); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nasDet, "对冲腿：无") {
		t.Fatalf("无腿注记缺: %s", nasDet)
	}
	// 幂等重跑：hedged 行数不变、注记不重复追加。
	hedgeScan(ctx, st)
	var hedged2 int
	_ = st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_opp_hypothesis WHERE category_key LIKE 'cfd_swap_hedged|d514m1|%'`).Scan(&hedged2)
	if hedged2 != 2 {
		t.Fatalf("重跑重产: %d", hedged2)
	}
	_ = st.Pool().QueryRow(ctx,
		`SELECT detail FROM fin_opp_hypothesis WHERE category_key='cfd_swap|d514m1|NAS|long'`).Scan(&nasDet)
	if strings.Count(nasDet, "对冲腿：无") != 1 {
		t.Fatalf("注记重复: %s", nasDet)
	}
}

// TestDelegateRetire 退役闸锚：live+零产 15d+零持仓 → retired+warn；活跃/在持仓
// 不误伤；撤键即全停。
func TestDelegateRetire(t *testing.T) {
	st, ctx := d514Store(t)
	for _, q := range []string{
		`DELETE FROM fin_strategy WHERE strategy LIKE 'D514RET%'`,
		`DELETE FROM strategy_stats WHERE strategy LIKE 'D514RET%'`,
		`DELETE FROM app_config WHERE key LIKE 'retire_%'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// XASSET_CARRY 为 positions.type 枚举内值，借作在仓策略名；先存原态恢复。
	var xcPrev *string
	{
		var v string
		if err := st.Pool().QueryRow(ctx,
			`SELECT state FROM fin_strategy WHERE strategy='XASSET_CARRY'`).Scan(&v); err == nil {
			xcPrev = &v
		}
	}
	defer func() {
		for _, q := range []string{
			`DELETE FROM fin_strategy WHERE strategy LIKE 'D514RET%'`,
			`DELETE FROM strategy_stats WHERE strategy LIKE 'D514RET%'`,
			`DELETE FROM app_config WHERE key LIKE 'retire_%'`,
			`DELETE FROM positions WHERE type='XASSET_CARRY' AND opportunity_id='d514ret'`,
			`DELETE FROM fin_proposal WHERE template_id LIKE 'D514RET%'`,
		} {
			_, _ = st.Pool().Exec(ctx, q)
		}
		if xcPrev != nil {
			_, _ = st.Pool().Exec(ctx,
				`UPDATE fin_strategy SET state=$1 WHERE strategy='XASSET_CARRY'`, *xcPrev)
		} else {
			_, _ = st.Pool().Exec(ctx,
				`DELETE FROM fin_strategy WHERE strategy='XASSET_CARRY'`)
		}
	}()
	for _, q := range []string{
		`INSERT INTO fin_strategy (strategy, state, decided_at, updated_at) VALUES
		 ('D514RET_IDLE','live',now()-interval '30 days',now()-interval '30 days'),
		 ('D514RET_ACT','live',now(),now()),
		 ('XASSET_CARRY','live',now(),now())
		 ON CONFLICT (strategy) DO UPDATE SET state='live'`,
		`INSERT INTO strategy_stats (strategy, total_pnl) VALUES
		 ('D514RET_IDLE',0),('D514RET_ACT',0),('XASSET_CARRY',0)
		 ON CONFLICT (strategy) DO UPDATE SET total_pnl=0`,
		`INSERT INTO fin_proposal (template_id, exec_track, venue, status, created_at)
		 VALUES ('D514RET_ACT','auto','binance','proposed',now())`,
		`INSERT INTO positions (opportunity_id, type, status) VALUES
		 ('d514ret','XASSET_CARRY','OPEN')`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	delegateRetire(ctx, st)
	state := func(s string) string {
		var v string
		if err := st.Pool().QueryRow(ctx,
			`SELECT state FROM fin_strategy WHERE strategy=$1`, s).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if state("D514RET_IDLE") != "retired" {
		t.Fatalf("零产 live 应退役: %s", state("D514RET_IDLE"))
	}
	if state("D514RET_ACT") != "live" || state("XASSET_CARRY") != "live" {
		t.Fatalf("误伤: act=%s open=%s", state("D514RET_ACT"), state("XASSET_CARRY"))
	}
	var by string
	if err := st.Pool().QueryRow(ctx,
		`SELECT decided_by FROM fin_strategy WHERE strategy='D514RET_IDLE'`).Scan(&by); err != nil {
		t.Fatal(err)
	}
	if by != "delegate-retire" {
		t.Fatalf("decided_by: %s", by)
	}
	// 撤键：重置 IDLE2 同样零产，关掉开关→不退。
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_strategy (strategy, state) VALUES ('D514RET_IDLE2','live');
		INSERT INTO strategy_stats (strategy, total_pnl) VALUES ('D514RET_IDLE2',0);
		INSERT INTO app_config (key, value) VALUES ('retire_delegate_enabled','0')
		ON CONFLICT (key) DO UPDATE SET value='0'`); err != nil {
		t.Fatal(err)
	}
	delegateRetire(ctx, st)
	if state("D514RET_IDLE2") != "live" {
		t.Fatal("撤键后仍退役")
	}
	// pnl 硬线：负 PnL 零持仓零活动 → 退。
	if _, err := st.Pool().Exec(ctx, `
		DELETE FROM app_config WHERE key='retire_delegate_enabled';
		INSERT INTO fin_strategy (strategy, state) VALUES ('D514RET_LOSS','live');
		INSERT INTO strategy_stats (strategy, total_pnl) VALUES ('D514RET_LOSS',-100)`); err != nil {
		t.Fatal(err)
	}
	delegateRetire(ctx, st)
	if state("D514RET_LOSS") != "retired" {
		t.Fatal("亏损硬线未触发")
	}
}
