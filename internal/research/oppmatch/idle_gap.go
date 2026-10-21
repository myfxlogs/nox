// idle_gap.go —— 闲置 CEX 池扫描（okx idle 缺口行+earn 多 venue 化两票并径）：
// Idle 池∈earnExecVenues（binance/okx/gate rail 面）→venue 键 cash_mgmt 提案；
// 面外 venue→fin_condition_gap executor 缺口+请求箱推（不臆造 venue API）。
// <$100 尘额两径都不入（尘池骚扰收益不抵）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// idleGapDustUSD 粉尘阈：闲置权益 <$100 不出缺口行（okx:earn $169 类尘额
// 手工 waived 实证——尘池缺口骚扰收益不抵）。
const idleGapDustUSD = 100.0

// idleGapUSDish USD 族币种判定（尘阈适用域——原生量不可跨币种与 $100 比；
// 与 binanceStablePx 同族口径：USD 锚稳定币视为同记帐单位非编）。
func idleGapUSDish(ccy string) bool {
	switch ccy {
	case "USD", "USDT", "USDC", "BUSD", "FDUSD", "DAI", "TUSD", "USDP", "USD1", "BFUSD":
		return true
	}
	return false
}

// idleGapScan 闲置 CEX 池缺口扫描（RunOnce 扫描拍尾挂）：CEX 池（非 mt 非 ghost）
// Idle=true 且权益>0 且 venue 无 earn 执行径→OpenConditionGap+RequestConditionGap
// （幂等——同键在册/已 requested 零重推）。读败仅 log 不阻塞扫描拍。
func idleGapScan(ctx context.Context, st *pgstore.Store) {
	pools, err := st.ExplorerIdleFunds(ctx, time.Now())
	if err != nil {
		log.Printf("[oppmatch] idle gap pools: %v", err)
		return
	}
	lm := fetchLiveEquityMap(ctx) // D-604 P2③：拍首单次实拉（判定径禁快照现值）
	cashLive := map[string]bool{} // D-523⑥：本轮仍持款的 cash_mgmt venue 集
	for _, p := range pools {
		if p.Ghost || p.VenueType == "mt4" || p.VenueType == "mt5" || !p.Idle {
			continue
		}
		// D-604 P2③：无流池判定输入=消费点实拉值（拉败/映射缺=如实跳过非陈值）。
		eq, ok := lm.poolEquity(p.Venue, p.VenueType, p.Currency, p.Equity, "[oppmatch]")
		// 尘阈是 USD 口径——只对 USD 族币种池比（多币池原生量与 $100 比较=
		// 单位混淆，NAV 指纹票同审项）；非 USD 族池放行后续币种闸裁决。
		if !ok || (idleGapUSDish(p.Currency) && eq < idleGapDustUSD) {
			continue
		}
		p.Equity = eq
		p.Balance = eq
		// 有执行径的闲池（earnExecVenues 面）→直接产 cash_mgmt 提案（pending 唯一锚
		// 1024 含 venue——同模板异所各行其位幂等）。USDT 先行域（earn 提案定值域）。
		if execVenueSets(ctx, st)["cash_mgmt"][p.Venue] { // D-651 投影

			if p.Currency != "USDT" {
				continue
			}
			// APR 下限闸（承接位提案噪音根治——484 expired 实证）：先读 APR 后建提案，
			// 低于 cash_mgmt_min_apr（app_config 缺省 0.005=0.5%，分数域同 fin_cex_earn_apr.apr）或缺 APR 不提案，
			// 假设层 UpsertHypothesis 幂等记实（低承接承接位不提案但留痕不静默）；
			// 不进 cashLive=既存 pending 提案同拍 expired（噪面自然消）。
			apr, aprOK, aprErr := st.LatestEarnAPR(ctx, p.Venue, p.Currency)
			if aprErr != nil {
				log.Printf("[oppmatch] idle apr %s: %v", p.Venue, aprErr)
			}
			minApr := 0.005
			if v, ok, cerr := st.AppConfigFloat(ctx, "cash_mgmt_min_apr"); cerr == nil && ok {
				minApr = v
			}
			if !aprOK || apr < minApr {
				why := "无 APR 行"
				if aprOK {
					why = fmt.Sprintf("APR %.3f<阈 %.3f（分数域）", apr, minApr)
				}
				if _, _, _, herr := st.UpsertHypothesis(ctx, pgstore.OppHypothesis{
					Venue: p.Venue, CategoryKey: "cash_mgmt:low_apr:" + p.Venue,
					InstType: "cash_mgmt", Shape: "earn_apr",
					Sample: fmt.Sprintf("%.4f", apr),
					Detail: fmt.Sprintf("承接位闲池 %.4g USDT 不提案：%s（假设层如实记录，APR 回升即自然复提案）",
						p.Equity, why)}); herr != nil {
					log.Printf("[oppmatch] idle hypothesis %s: %v", p.Venue, herr)
				}
				log.Printf("[oppmatch] idle %s 不提案：%s", p.Venue, why)
				continue
			}
			cashLive[p.Venue] = true
			pid, _, err := st.CreateProposal(ctx, &pgstore.Proposal{
				TemplateID: "cash_mgmt", ExecTrack: "auto", Venue: p.Venue})
			if err != nil {
				log.Printf("[oppmatch] idle proposal %s: %v", p.Venue, err)
				continue
			}
			// 提案须带评估卡（M4.1 无卡拒单——SG 实证 #377/#378 零卡死提案）。
			// 证源=池闲置实证+venue earn APR 最新行（闸已先读——此处同源填卡）。
			r := Result{TemplateID: "cash_mgmt", ExecTrack: "auto", APR: &apr}
			r.GrossEvidence = fmt.Sprintf(
				"fin_cex_earn_apr %s %s 最新行；闲置池实证 equity=%.4g 30d收益=0",
				p.Venue, p.Currency, p.Equity)
			card := evalProposal(ctx, st, r, pid)
			if _, uerr := st.UpsertOppEval(ctx, card); uerr != nil {
				log.Printf("[oppmatch] idle eval card #%d: %v", pid, uerr)
			}
			continue
		}
		subj := p.Venue + ":" + p.VenueType
		id, err := st.OpenConditionGap(ctx, &pgstore.ConditionGap{
			TemplateID: "cash_mgmt", Kind: "executor", Subject: subj,
			RequestText: fmt.Sprintf(
				"%s 池闲置 %.4g %s（equity %.4g，30d 收益=0）——%s 无 earn 申购执行径；"+
					"请 venue 实查理财写面并立票接径，或人工划转至可承接 venue", p.Venue, p.Balance,
				strings.ToUpper(p.Currency), p.Equity, subj),
		})
		if err != nil {
			log.Printf("[oppmatch] idle gap open %s: %v", subj, err)
			continue
		}
		if err := st.RequestConditionGap(ctx, id); err != nil {
			log.Printf("[oppmatch] idle gap request %s: %v", subj, err)
		}
	}
	// D-523⑥：目标款已消失（池不再闲/权益<尘阈）→同拍 expired 不挂 SLA；
	// 决策面由 expired 态如实提示（existed-but-dead 提案不再占待裁面）。
	liveVenues := make([]string, 0, len(cashLive))
	for v := range cashLive {
		liveVenues = append(liveVenues, v)
	}
	// expire 粘性修：连 N 拍 miss 才杀（app_config cash_mgmt_miss_threshold 缺省 3——
	// 单拍瞬态 miss 不再灭提案=491 病灶修法；真消失 N 拍后仍死）
	missThr := 3
	if v, ok, err := st.AppConfigInt64(ctx, "cash_mgmt_miss_threshold"); err == nil && ok && v > 0 {
		missThr = int(v)
	}
	if _, n, err := st.BumpProposalMissOutsideVenues(ctx, "cash_mgmt", liveVenues, missThr); err != nil {
		log.Printf("[oppmatch] cash target expire: %v", err)
	} else if n > 0 {
		log.Printf("[oppmatch] cash_mgmt target gone %d 拍连 miss → expired %d proposals", missThr, n)
	}
}

// liveEquityMapFn D-604 P2③：无流池消费点批量实拉钩（cmd/core 装配——
// 扫描拍首单次 Refresh→venue:type→equity 映射；nil=快照行旧径保旧行为）。
// 拉取败=该池跳过+warn 留痕（§6#4 禁显示陈值禁静默）；禁 TTL 变相快照。
var liveEquityMapFn func(ctx context.Context) (map[string]float64, error)

func SetLiveEquityMapFn(fn func(ctx context.Context) (map[string]float64, error)) {
	liveEquityMapFn = fn
}

// liveEquityMap 拍首单次实拉结果（退回㉗伤③修法：Refresh 在消费拍首提升一次
// 得 map 传引用——禁逐池逐调全量 Refresh 风暴；map 拍作用域，禁跨拍缓存）。
type liveEquityMap struct {
	m   map[string]float64
	err error
}

// fetchLiveEquityMap 拍首调用一次（未接线=零值 liveEquityMap，消费点照旧快照行）。
func fetchLiveEquityMap(ctx context.Context) liveEquityMap {
	if liveEquityMapFn == nil {
		return liveEquityMap{}
	}
	m, err := liveEquityMapFn(ctx)
	return liveEquityMap{m: m, err: err}
}

// poolEquity 无流池实拉值取用（venueType 空/mt4/mt5=非无流池——快照行合法直返）。
// 键=venue:type:ccy 分币粒度（NAV 指纹票——MULTI 户指纹禁入映射，池按币种取
// 原生量）。拉败/映射缺=跳过该池（fail-closed 如实缺——§6#4 warn 留痕，
// tag=消费点日志前缀）。
func (lm liveEquityMap) poolEquity(venue, venueType, ccy string, snap float64, tag string) (float64, bool) {
	if venueType == "" || venueType == "mt4" || venueType == "mt5" || (lm.m == nil && lm.err == nil) {
		return snap, true
	}
	if lm.err != nil {
		log.Printf("%s live equity map: %v（如实跳过·warn 留痕）", tag, lm.err)
		return 0, false
	}
	key := venue + ":" + venueType + ":" + ccy
	eq, ok := lm.m[key]
	if !ok {
		log.Printf("%s live equity %s 映射缺（跳过·warn 留痕）", tag, key)
		return 0, false
	}
	return eq, true
}
