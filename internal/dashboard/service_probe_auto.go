// service_probe_auto.go —— D-163：探针提案机审（自动化审批闭环）。
// 触发=提案落库 kick（explorer → bus.Topic）+ boot 扫（停机窗提案不漏）；无定时轮询。
// 判定序（全 fail-closed）：
//
//	① 契约复读（validateProbeContractForOpen 同一函数，不复制守卫）→ 硬伤拒
//	② cap：expected_cost ≤ ARB_PROBE_MAX_COST、在途 OPEN PROBE < ARB_PROBE_MAX_OPEN → 拒
//	③ premise 回放：basis 按段（；/;）前缀判别——
//	   term_dispersion → 鲜 funding facts 重算跨 venue min/max（贡献腿任一根
//	     facts 超 2×interval[funding_interval 缺省 8h×2] 或鲜腿<2 或离散塌缩 → 硬伤拒）
//	   idle_funds → venue 权益快照在且 ≥ expected_cost（快照缺=歧义，eq<cost=硬伤）
//	   未知前缀 → 歧义（留 proposed + warn 决策席，不猜）
//	④ 可执行性：报价鲜帧+原生符号、universe TRADING（CEX 腿）、venue 流已知则须在线、
//	   perp 腿自身 funding facts 鲜 → 硬伤拒
//
// 全绿 → approveProbeOpen（同一 CAS+OpenProbe 守卫链，decision_ref=D-163）。
// 硬伤 → ProbeProposalReject（CAS，理由落 gate_note）。歧义 → 留 proposed +
// gate_note 幂等键（同判据不重复 warn）。
// ARB_PROBE_RPC_ENABLED 主闸：未开=整机审不跑（OpenProbe 同闸双保险）。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// probeAutoRef D-163 机审 decision_ref（validator ^D-\d{3,}$ 合法形）。
const probeAutoRef = "D-163"

// probeFundingDefaultInterval funding_interval facts 缺/坏时的缺省周期（8h——
// CEX funding 主流周期；只用作新鲜度界 2×周期，不改写任何数值）。
const probeFundingDefaultInterval = 8 * time.Hour

// RunProbeAdjudicator 机审循环：boot 全量扫一次（停机窗提案不漏），
// 之后 kick 驱动（explorer 提案落库即发；慢消费者丢旧保新由 Topic 保证）。
func (s *Service) RunProbeAdjudicator(ctx context.Context, sub <-chan struct{}) {
	s.adjudicateProbes(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sub:
			if !ok {
				return
			}
			s.adjudicateProbes(ctx)
		}
	}
}

// adjudicateProbes 全量扫 status='proposed' 逐条裁定。RPC 主闸未开/未装配 → 不审。
func (s *Service) adjudicateProbes(ctx context.Context) {
	if s.store == nil {
		return
	}
	cfg := s.acv.running
	if cfg == nil || !cfg.ProbeRPCEnabled {
		return
	}
	list, err := s.store.ExplorerProbeProposalList(ctx, "proposed")
	if err != nil {
		log.Printf("[probe-auto] list proposed: %v", err)
		return
	}
	for _, p := range list {
		s.adjudicateOne(ctx, p, cfg.ProbeMaxCostUSD, cfg.ProbeMaxOpen)
	}
}

// adjudicateOne 单条裁定：硬伤拒 / 歧义留痕 / 全绿批准开仓。
func (s *Service) adjudicateOne(ctx context.Context, p *pgstore.ProbeProposal, maxCost float64, maxOpen int) {
	reject, ambiguous, note := s.probeGateVerdict(ctx, p, maxCost, maxOpen)
	switch {
	case reject != "":
		reason := probeAutoRef + " 机审拒: " + reject
		ok, err := s.store.ProbeProposalReject(ctx, p.ID, reason)
		if err != nil {
			log.Printf("[probe-auto] #%d reject: %v", p.ID, err)
			return
		}
		if ok {
			log.Printf("[probe-auto] #%d rejected: %s", p.ID, reject)
		} // !ok=并发已处，静默幂等
	case ambiguous != "":
		note = probeAutoRef + " 机审歧义留决: " + ambiguous
		if p.GateNote == note { // 同判据已留——不重复 warn（幂等）
			return
		}
		if err := s.store.ProbeProposalGateNote(ctx, p.ID, note); err != nil {
			log.Printf("[probe-auto] #%d gate_note: %v", p.ID, err)
		}
		msg := fmt.Sprintf("[probe-auto] 提案 #%d 待人工裁定: %s", p.ID, ambiguous)
		if err := s.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
			log.Printf("[probe-auto] #%d warn: %v", p.ID, err)
		}
	default:
		resp, err := s.approveProbeOpen(ctx, p, probeAutoRef)
		if err != nil {
			log.Printf("[probe-auto] #%d approve: %v", p.ID, err)
			return
		}
		if nerr := s.store.ProbeProposalGateNote(ctx, p.ID,
			probeAutoRef+" 机审通过: "+note+" → "+resp.GetMessage()); nerr != nil {
			log.Printf("[probe-auto] #%d gate_note: %v", p.ID, nerr)
		}
		log.Printf("[probe-auto] #%d %s", p.ID, resp.GetMessage())
	}
}

// probeGateVerdict 门禁判定。返回三态恰一非空：reject=硬伤拒 / ambiguous=歧义留决 /
// note=全绿判据注记。
func (s *Service) probeGateVerdict(ctx context.Context, p *pgstore.ProbeProposal, maxCost float64, maxOpen int) (reject, ambiguous, note string) {
	// ① 契约复读（RPC 同一函数）。
	if err := validateProbeContractForOpen(p); err != nil {
		return err.Error(), "", ""
	}
	c := p.Contract
	// ② cap：声明成本 + 在途数（app_config 链）。
	if maxCost > 0 && c.ExpectedCostUSD > maxCost {
		return fmt.Sprintf("expected_cost $%.2f > 上限 $%.2f", c.ExpectedCostUSD, maxCost), "", ""
	}
	if maxOpen > 0 {
		active, err := s.store.CountActiveProbes(ctx)
		if err != nil {
			return fmt.Sprintf("在途探针计数失败: %v", err), "", ""
		}
		if active >= maxOpen {
			return fmt.Sprintf("在途探针 %d ≥ 上限 %d", active, maxOpen), "", ""
		}
	}
	// ③ premise 回放：结构化 premises 逐条判（D-176——散文 basis 不再解析，
	// 「段」字段落标签被当 canonical 吞吃的假歧义同源修）。任一硬伤 → 拒；任一歧义 → 留决。
	var rejects, ambis, notes []string
	for _, prem := range c.Premises {
		r, a, n := s.probePremiseSeg(ctx, p, prem)
		switch {
		case r != "":
			rejects = append(rejects, r)
		case a != "":
			ambis = append(ambis, a)
		default:
			notes = append(notes, n)
		}
	}
	if len(rejects) > 0 {
		return strings.Join(rejects, "；"), "", ""
	}
	if len(ambis) > 0 {
		return "", strings.Join(ambis, "；"), ""
	}
	// ④ 可执行性（perp 腿自身 funding facts 鲜 + 报价鲜帧 + universe TRADING + 流在线）。
	if r := s.probeLegExecutable(ctx, p); r != "" {
		return r, "", ""
	}
	return "", "", strings.Join(notes, "；")
}
