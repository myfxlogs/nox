// probe.go —— D-048①：scan_kind='probe' 提案契约校验 + 落库 + 待审批推送。
// 契约九字段全必填（purpose/falsify/venue_key/canonical/side/qty/notional_usd/
// expected_cost_usd/auto_close_at/basis）——缺任一字段拒入库+log 拒因。
// 「待审批探针」走 warn alert → wireFeishuPush 扇出飞书（非 info 级全推，alerts 表留痕）。
package explorer

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	explorerv1 "arb/internal/gen/arb/explorer/v1"
	"arb/internal/store/pgstore"
)

// probeFalsifyDigitRe 证伪标准「可量化」底线校验——至少含一个数字锚
// （阈值/比率/金额；纯文字「看 swap 大不大」不可证伪，拒收）。
var probeFalsifyDigitRe = regexp.MustCompile(`\d`)

// probeMaxNotionalDefault ARB_PROBE_MAX_NOTIONAL 行缺时缺省（与 config envFloat 缺省同源=1000）。
const probeMaxNotionalDefault = 1000.0

// probeMaxNotional 探针名义上限热读（app_config 链）。err → 告警 + false
// （fail-closed：上限不可读时本轮探针提案全拒，不放行无上限契约）。
func probeMaxNotional(ctx context.Context, deps Deps) (float64, bool) {
	v, ok, err := deps.Store.AppConfigFloat(ctx, "ARB_PROBE_MAX_NOTIONAL")
	if err != nil {
		deps.alert(ctx, "warn", "[探索] 探针名义上限读取失败（本轮探针提案全拒）: "+err.Error())
		return 0, false
	}
	if !ok || v <= 0 {
		return probeMaxNotionalDefault, true
	}
	return v, true
}

// validateProbeContract 行级契约校验。返回 (contract, rejectReason)——
// reason 非空=该行拒入库（调用方 log 拒因，缺字段绝不静默丢）。
func validateProbeContract(p *explorerv1.ProbeProposal, known map[string]bool, maxN float64, capOK bool, now time.Time) (*pgstore.ProbeContract, string) {
	if p == nil {
		return nil, "空行"
	}
	c := &pgstore.ProbeContract{
		Purpose:   strings.TrimSpace(p.GetPurpose()),
		Falsify:   strings.TrimSpace(p.GetFalsify()),
		VenueKey:  strings.TrimSpace(p.GetVenueKey()),
		Canonical: strings.TrimSpace(p.GetCanonical()),
		Side:      strings.TrimSpace(p.GetSide()),
		Basis:     strings.TrimSpace(p.GetBasis()),
	}
	switch {
	case c.Purpose == "":
		return nil, "缺 purpose（目的）"
	case c.Falsify == "" || !probeFalsifyDigitRe.MatchString(c.Falsify):
		return nil, "缺/不可量化 falsify（须含数字阈值）"
	case c.VenueKey == "":
		return nil, "缺 venue_key"
	case c.Canonical == "" || !known[c.Canonical]:
		return nil, "canonical 缺或不在已知集"
	case c.Side != "BUY" && c.Side != "SELL":
		return nil, "side 须 BUY|SELL"
	case p.Qty == nil || p.GetQty() <= 0:
		return nil, "qty 缺或 ≤0"
	case p.NotionalUsd == nil || p.GetNotionalUsd() <= 0:
		return nil, "notional_usd 缺或 ≤0"
	case !capOK:
		return nil, "名义上限读取失败（fail-closed 全拒）"
	case p.GetNotionalUsd() > maxN:
		return nil, fmt.Sprintf("notional_usd %.2f > 上限 %.2f", p.GetNotionalUsd(), maxN)
	case p.ExpectedCostUsd == nil || p.GetExpectedCostUsd() < 0:
		return nil, "expected_cost_usd 缺或 <0"
	case p.AutoCloseAt == nil || !p.GetAutoCloseAt().IsValid():
		return nil, "auto_close_at 缺/非法"
	case !p.GetAutoCloseAt().AsTime().After(now):
		return nil, "auto_close_at 不在未来"
	case c.Basis == "":
		return nil, "缺 basis（数据依据）"
	case len(p.GetPremises()) == 0:
		return nil, "缺 premises（结构化前提——D-176 机审唯一判据源，散文 basis 不再解析）"
	}
	for _, prem := range p.GetPremises() {
		if prem.GetKind() == "" {
			return nil, "premise 缺 kind"
		}
	}
	c.Premises = make([]pgstore.ProbePremise, 0, len(p.GetPremises()))
	for _, prem := range p.GetPremises() {
		c.Premises = append(c.Premises, pgstore.ProbePremise{
			Kind:      strings.TrimSpace(prem.GetKind()),
			Venue:     strings.TrimSpace(prem.GetVenue()),
			Canonical: strings.TrimSpace(prem.GetCanonical()),
			FreeUSD:   prem.GetFreeUsd(),
		})
	}
	c.Qty = p.GetQty()
	c.NotionalUSD = p.GetNotionalUsd()
	c.ExpectedCostUSD = p.GetExpectedCostUsd()
	c.AutoCloseAt = p.GetAutoCloseAt().AsTime()
	return c, ""
}

// runProbeProposals 契约校验 → 逐条落库 → 每条新行推「待审批探针」。
// 返回 (inserted, dropped)。dedupe 命中（同日同 canonical+venue 重跑）幂等跳过不再推。
func runProbeProposals(ctx context.Context, deps Deps, batch *explorerv1.ProposalBatch,
	known map[string]bool, now, scanDate time.Time) (inserted, dropped int) {
	maxN, capOK := 0.0, true
	if len(batch.GetProbeProposals()) > 0 {
		maxN, capOK = probeMaxNotional(ctx, deps)
	}
	for _, p := range batch.GetProbeProposals() {
		c, why := validateProbeContract(p, known, maxN, capOK, now)
		if c == nil {
			dropped++
			log.Printf("[explorer] probe proposal drop: %s (venue=%s canonical=%s side=%s)",
				why, p.GetVenueKey(), p.GetCanonical(), p.GetSide())
			continue
		}
		id, ok, err := deps.Store.ExplorerProbeProposalInsert(ctx, *c, scanDate)
		if err != nil {
			dropped++
			log.Printf("[explorer] probe proposal insert: %v", err)
			continue
		}
		if !ok {
			continue
		}
		inserted++
		deps.alert(ctx, "warn", fmt.Sprintf(
			"[待审批探针] #%d %s %s %s qty=%.6g 名义≈$%.0f 成本≈$%.2f 到期=%s falsify=%s",
			id, c.VenueKey, c.Canonical, c.Side, c.Qty, c.NotionalUSD, c.ExpectedCostUSD,
			c.AutoCloseAt.In(BJTime).Format("01-02 15:04"), c.Falsify))
	}
	if inserted > 0 && deps.ProbeKick != nil {
		deps.ProbeKick() // D-163：新提案唤醒机审（非阻塞发布，丢旧保新）
	}
	return inserted, dropped
}
