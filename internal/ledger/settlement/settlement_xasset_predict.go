// settlement_xasset_predict.go D-475a S7：XASSET_CARRY 预测-实测对照（G11）。
// 对账基线=SumCashflowsNet（funding+swap+commission——与纸面 accrued 同口径；
// realized_pnl 含双腿价差盈亏噪声不可比，P1 修稿钉死）。偏离>50% → warn flag
// 自动写（同键已有 active 旗跳过）+warn alert。观测面非钱闸：一切失败只 log。
package settlement

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// checkXassetPredictVsActual won 落账后调用：p.Type=XASSET_CARRY 才走；
// DedupKey=combo.Key（'XASSET_CARRY:canonical:venueA_key:venueB_key'）解析
// canonical+两腿裸名→LatestPaperByLegs 回查纸面行→偏离率判定。
func checkXassetPredictVsActual(ctx context.Context, deps Deps, p *pgstore.Position, posID int64, cashflowsNet float64) {
	if p.Type != "XASSET_CARRY" {
		return
	}
	key := p.DedupKey
	if key == "" { // 老仓 DedupKey 空→机会 ID 同格式兜底（'TYPE:canonical:vA:vB'）
		key = p.OpportunityID
	}
	canonical, legVenues := parseXassetDedupKey(key)
	if canonical == "" || len(legVenues) != 2 {
		log.Printf("[settlement] pos=%d predict-check: key %q 解析失败跳过", posID, key)
		return
	}
	paper, err := deps.LatestPaperByLegs(ctx, canonical, legVenues)
	if err != nil || paper == nil {
		return // 无匹配 paper 行→跳过（非毕业通道仓不对照）
	}
	deviation := math.Abs(cashflowsNet-paper.AccruedUSD) / math.Max(math.Abs(paper.AccruedUSD), 1)
	if deviation <= 0.5 {
		return
	}
	// 同键已有 active 旗→跳过（SkipDuplicates 语义，不刷屏）。
	if flags, ferr := deps.ListActiveFlags(ctx); ferr == nil {
		if _, ok := flags[pgstore.FlagKey(paper.VenuePair(), paper.Symbol)]; ok {
			return
		}
	}
	reason := fmt.Sprintf("实测偏离预测 %.0f%% pos#%d（cashflows_net=%.2f vs paper accrued=%.2f）",
		deviation*100, posID, cashflowsNet, paper.AccruedUSD)
	if err := deps.InsertFlag(ctx, &pgstore.VenueSymbolFlag{
		VenueA: paper.VenueA, VenueB: paper.VenueB, Symbol: paper.Symbol, Level: "warn", Reason: reason,
	}); err != nil {
		log.Printf("[settlement] pos=%d predict-check flag: %v", posID, err)
		return
	}
	if err := deps.InsertAlert(ctx, nil, "warn",
		fmt.Sprintf("[校准回流] %s×%s %s", paper.VenuePair(), paper.Symbol, reason)); err != nil {
		log.Printf("[settlement] pos=%d predict-check alert: %v", posID, err)
	}
}

// parseXassetDedupKey 'TYPE:canonical:vA_key:vB_key' 拆分——venue_key 自身含 ':'
// （'3001157:mt5'），取 canonical 后余段对半分两 venue_key（各占偶数段）。
func parseXassetDedupKey(key string) (string, []string) {
	parts := strings.Split(key, ":")
	if len(parts) < 4 || len(parts[2:])%2 != 0 {
		return "", nil
	}
	rest := parts[2:]
	half := len(rest) / 2
	bare := keydomain.BareOf // venue_key→裸名（paper venue 组合串=裸名×裸名）
	return parts[1], []string{
		bare(strings.Join(rest[:half], ":")),
		bare(strings.Join(rest[half:], ":")),
	}
}
