// deadcorner.go —— 死角日报（STATE 票）：daemon/采集流/策略三面「在跑零产出」对拍。
// 纯报告件——只读已采数据，不拦不拒不动任何径；daemon→产出域映射=手写小 map
// （票面钦定不通用化）；N=0 照常印一行防静默。
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// deadOut daemon→产出域表映射（Name=fin_employee_registry.name；表名列名=白名单
// 常量拼 SQL 标识符不参数化，值域固定）。
type deadOut struct {
	Daemon string
	Table  string
	TsCol  string
	Where  string // 附加谓词（如 venue 收窄），可空
}

// daemonOutputs 在编 daemon 的周期产出面。只收「每日应产出行」的件——纯事件驱动
// 件（cex-io 出入账）不入图防误报。收入采集=仓位结算驱动，空仓期零行如实列名
// （票面口径：孤儿清单如实列不藏）。
var daemonOutputs = []deadOut{
	{"main:cexscan", "fin_cex_earn_apr", "ts", ""},
	{"main:acctFrameSnapshots", "fin_venue_account_snapshot", "fetched_at", ""},
	{"main:ticker", "ticks", "ts", ""},
	{"main:income", "income_events", "ts", "venue='binance'"},
	{"main:okx_income", "income_events", "ts", "venue='okx'"},
	{"main:carry-universe", "fin_cex_universe", "last_seen", ""},
	{"main:oppmatch", "fin_proposal", "created_at", ""},
	{"setup:swapLedger", "position_cashflows", "ts", ""},
}

// consumedFactKinds facts kind→已有消费件的集合（读径实查 LatestFacts/读件在案）。
// 不在集合内的 fresh kind=「采集在跑消费断档」如实列孤儿。
var consumedFactKinds = map[string]bool{
	"funding": true, "funding_interval": true, "funding_next_settle": true,
	"swap_long": true, "swap_short": true, "delivery_depth_spread_bps": true,
	"funding_history": true, "term_dispersion": true, "idle_funds": true,
	"price": true, "cex_exposure_pct": true,
	"index_price": true, "vol24h": true, "order_history": true,
}

// deadCorner 死角行（face=A daemon / B facts 孤儿 / C 策略）。
type deadCorner struct {
	Face string
	Name string
	Why  string
}

func loadDeadCorners(ctx context.Context, pool *pgxpool.Pool) ([]deadCorner, error) {
	var out []deadCorner
	// A：在编 daemon running∧beat 新鲜 但产出域表 24h 零新行。
	rows, err := pool.Query(ctx, `
		SELECT name, cadence_sec, running, beat_ts, enabled FROM fin_employee_registry`)
	if err != nil {
		return nil, fmt.Errorf("deadcorner employees: %w", err)
	}
	duty := map[string]bool{}
	for rows.Next() {
		var name string
		var cadence int
		var running, enabled bool
		var beat *time.Time
		if err := rows.Scan(&name, &cadence, &running, &beat, &enabled); err != nil {
			rows.Close()
			return nil, fmt.Errorf("deadcorner employee scan: %w", err)
		}
		fresh := cadence <= 0 || (beat != nil && time.Since(*beat) <= 3*time.Duration(cadence)*time.Second)
		duty[name] = enabled && running && fresh
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, d := range daemonOutputs {
		on, ok := duty[d.Daemon]
		if !ok || !on {
			continue // 不在册/不在职：离线面另有滞跳报，死角只管「在跑零产出」
		}
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s >= now()-interval '24 hours'`, d.Table, d.TsCol)
		if d.Where != "" {
			q += " AND " + d.Where
		}
		var n int64
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			return nil, fmt.Errorf("deadcorner count %s.%s: %w", d.Table, d.TsCol, err)
		}
		if n == 0 {
			out = append(out, deadCorner{Face: "daemon", Name: d.Daemon, Why: "在职但产出域 " + d.Table + " 24h 零新行"})
		}
	}
	// B：facts kind 24h 有采集但无消费件在案。
	krows, err := pool.Query(ctx, `
		SELECT kind, COUNT(*) FROM facts WHERE ts >= now()-interval '24 hours'
		GROUP BY kind ORDER BY kind`)
	if err != nil {
		return nil, fmt.Errorf("deadcorner facts kinds: %w", err)
	}
	for krows.Next() {
		var kind string
		var n int64
		if err := krows.Scan(&kind, &n); err != nil {
			krows.Close()
			return nil, fmt.Errorf("deadcorner fact kind scan: %w", err)
		}
		if !consumedFactKinds[kind] {
			out = append(out, deadCorner{Face: "facts", Name: kind,
				Why: fmt.Sprintf("24h 采集 %d 行但无消费件在案", n)})
		}
	}
	krows.Close()
	if err := krows.Err(); err != nil {
		return nil, err
	}
	// C：fin_strategy live 但 48h 零提案（fin_proposal.template_id）零动作（autotrade_logs.strategy）。
	srows, err := pool.Query(ctx, `
		SELECT s.strategy FROM fin_strategy s WHERE s.state='live'
		  AND NOT EXISTS (SELECT 1 FROM autotrade_logs l
		    WHERE l.strategy=s.strategy AND l.ts >= now()-interval '48 hours')
		  AND NOT EXISTS (SELECT 1 FROM fin_proposal p
		    WHERE p.template_id=s.strategy AND p.created_at >= now()-interval '48 hours')
		ORDER BY s.strategy`)
	if err != nil {
		return nil, fmt.Errorf("deadcorner strategies: %w", err)
	}
	for srows.Next() {
		var name string
		if err := srows.Scan(&name); err != nil {
			srows.Close()
			return nil, fmt.Errorf("deadcorner strategy scan: %w", err)
		}
		out = append(out, deadCorner{Face: "strategy", Name: name, Why: "live 但 48h 零提案零动作"})
	}
	srows.Close()
	return out, srows.Err()
}

// renderDeadCornerSection 「死角」段——N=0 照常印一行（防静默票面钦定）。
func renderDeadCornerSection(cs []deadCorner) string {
	var buf strings.Builder
	buf.WriteString("\n## 死角对拍（在跑零产出——报告件不拦截）\n\n")
	buf.WriteString(fmt.Sprintf("死角：%d\n\n", len(cs)))
	if len(cs) == 0 {
		return buf.String()
	}
	buf.WriteString("| 面 | 名 | 状 |\n|---|---|---|\n")
	for _, c := range cs {
		buf.WriteString(fmt.Sprintf("| %s | %s | %s |\n", c.Face, c.Name, c.Why))
	}
	return buf.String()
}

// loadAdjudicationQueue 裁决队列浮面（D-521 续——proposed 积压是环境态非任务
// 面）：fin_opp_hypothesis status='proposed' 计数+分类键 top3。只读聚合件。
func loadAdjudicationQueue(ctx context.Context, pool *pgxpool.Pool) (int, []string, error) {
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM fin_opp_hypothesis WHERE status='proposed'`).Scan(&n); err != nil {
		return 0, nil, fmt.Errorf("adjudication queue count: %w", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT split_part(category_key,'|',1) AS k, count(*) c
		FROM fin_opp_hypothesis WHERE status='proposed'
		GROUP BY k ORDER BY c DESC, k LIMIT 3`)
	if err != nil {
		return 0, nil, fmt.Errorf("adjudication queue top: %w", err)
	}
	defer rows.Close()
	var tops []string
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			return 0, nil, err
		}
		tops = append(tops, fmt.Sprintf("%s×%d", k, c))
	}
	return n, tops, rows.Err()
}

// renderAdjudicationQueue 「待裁假设」行——N=0 照常印（同死角段防静默惯例）。
func renderAdjudicationQueue(n int, tops []string) string {
	if len(tops) == 0 {
		return fmt.Sprintf("待裁假设：%d 条（proposed）\n", n)
	}
	return fmt.Sprintf("待裁假设：%d 条（proposed，分类键 top3：%s）\n", n, strings.Join(tops, "、"))
}
