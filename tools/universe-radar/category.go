package main

// 新品类自发现管线 S1（设计稿 docs/新品类自发现管线设计.md）：
// 目录内新品型侦测（a 级源：新 instType/新形态键——venue 已返回但系统不收的品类）
// → fin_opp_hypothesis proposed 行（category_key UNIQUE 幂等+rejected 弃件记忆抑重推）
// → LLM 预审 memo（family_fit/params_proto/memo——顾问产 memo 不碰批准键）
// → info 告警聚合推。b 级 venue 公告 feed 缺位如实记（instType 全集无枚举端点，见 log）。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"nox/internal/store/pgstore"
)

// CategorySignal 目录内新品类信号（fin_opp_hypothesis 的原料行）。
type CategorySignal struct {
	CategoryKey string // venue|instType|shape——幂等锚
	Venue       string
	InstType    string // venue 原生仪器类（contractType/instType）
	Shape       string // 形态键（结算币/结构签名）
	Count       int    // 目录内该类行数
	Sample      string // 示例符号（≤3）
	Detail      string
}

// catSource 品类信号能力（Source 可选实现——不实现的所=无信号如实）。
type catSource interface {
	Categories(ctx context.Context) ([]CategorySignal, error)
}

// catAgg 信号聚合器：key→{count,≤3 样例} 保序。
type catAggEnt struct {
	n       int
	samples []string
}

type catAgg struct {
	m     map[string]*catAggEnt
	order []string
}

func newCatAgg() *catAgg { return &catAgg{m: map[string]*catAggEnt{}} }

func (c *catAgg) add(key, sample string) {
	a, ok := c.m[key]
	if !ok {
		a = &catAggEnt{}
		c.m[key] = a
		c.order = append(c.order, key)
	}
	a.n++
	if len(a.samples) < 3 {
		a.samples = append(a.samples, sample)
	}
}

// catLLM 可注入测试桩——生产面=analyst.RunWithSpecs 包装（role=category_memo 入 fin_llm_call）。
type catLLM func(ctx context.Context, system, prompt string) (string, error)

// processCategories S1 主管线：信号→假设行（幂等）→LLM memo（fail-open）→info 聚合告警。
// bootstrap 轮告警抑制（同 universe NEW 静默语义——首扫品类批落不洪泛）。
func processCategories(ctx context.Context, st *pgstore.Store, srcs []Source, llm catLLM, bootstrap bool) {
	var newLines []string
	for _, s := range srcs {
		cs, ok := s.(catSource)
		if !ok {
			continue
		}
		sigs, err := cs.Categories(ctx)
		if err != nil {
			log.Printf("[universe-radar] %s 品类信号读败（本所跳过）: %v", s.Venue(), err)
			continue
		}
		for _, sig := range sigs {
			h := pgstore.OppHypothesis{
				Venue: s.Venue(), CategoryKey: sig.CategoryKey,
				InstType: sig.InstType, Shape: sig.Shape,
				Sample: sig.Sample, Detail: sig.Detail,
			}
			id, inserted, memoPending, err := st.UpsertHypothesis(ctx, h)
			if err != nil {
				log.Printf("[universe-radar] hypothesis %s: %v", sig.CategoryKey, err)
				continue
			}
			// memoPending=上轮 memo 写败的死行（family_fit NULL）——重试预审不告警。
			if !inserted && !memoPending {
				continue // 键在册（proposed/rejected/trialing 不扰——弃件记忆+幂等锚）
			}
			ff, memo := runHypothesisMemo(ctx, st, llm, sig, id)
			if !inserted {
				continue // 死行回填不产告警（告警只认新键）
			}
			line := fmt.Sprintf("%s %s inst=%s shape=%s ×%d（样例 %s）family_fit=%s",
				s.Venue(), sig.CategoryKey, sig.InstType, sig.Shape, sig.Count, sig.Sample, ff)
			if memo != "" {
				if utf8.RuneCountInString(memo) > 300 {
					memo = string([]rune(memo)[:300]) + "…"
				}
				line += "｜" + strings.ReplaceAll(memo, "\n", " ")
			}
			newLines = append(newLines, line)
		}
	}
	if len(newLines) > 0 {
		log.Printf("[universe-radar] 新品类假设 %d 条（详情 fin_opp_hypothesis）", len(newLines))
		if !bootstrap {
			if err := st.InsertAlert(ctx, nil, "info",
				"[新品类假设] "+strings.Join(newLines, "；")); err != nil {
				log.Printf("[universe-radar] 品类告警落库败: %v", err)
			}
		}
	}
	backfillPendingMemos(ctx, st, llm)
}

// runHypothesisMemo D-623③：LLM 预审一段抽件——新键内联径与直写行回填径共用
// （memo 只顾问，批准键永不进 LLM 手；写败 log fail-open）。
func runHypothesisMemo(ctx context.Context, st *pgstore.Store, llm catLLM, sig CategorySignal, id int64) (ff, memo string) {
	proto := ""
	ff, memo, proto = "novel", "", ""
	if llm != nil {
		ff, memo, proto = catMemo(ctx, llm, sig)
	}
	if err := st.UpdateHypothesisMemo(ctx, id, ff, memo, proto); err != nil {
		log.Printf("[universe-radar] hypothesis memo %s: %v", sig.CategoryKey, err)
	}
	return ff, memo
}

// backfillPendingMemos D-623③：memo 径归一——每拍扫 proposed AND family_fit IS NULL
// 死行补预审，覆盖 swapScan/hedgeScan 等直写径产行（雷达键内联径之外的盲区）。
func backfillPendingMemos(ctx context.Context, st *pgstore.Store, llm catLLM) {
	if llm == nil {
		return
	}
	rows, err := st.ListMemoPendingHypotheses(ctx)
	if err != nil {
		log.Printf("[universe-radar] memo 死行枚举: %v", err)
		return
	}
	for _, h := range rows {
		sig := CategorySignal{
			CategoryKey: h.CategoryKey, Venue: h.Venue,
			InstType: h.InstType, Shape: h.Shape, Sample: h.Sample, Detail: h.Detail,
		}
		ff, memo, proto := catMemo(ctx, llm, sig)
		if err := st.UpdateHypothesisMemo(ctx, h.ID, ff, memo, proto); err != nil {
			log.Printf("[universe-radar] memo 回填 #%d: %v", h.ID, err)
		}
	}
}
