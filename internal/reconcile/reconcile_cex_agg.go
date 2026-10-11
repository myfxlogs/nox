// reconcile_cex_agg.go —— D-385 S1：CEX 聚合持仓对拍。
// CEX perp 仓位是账户级聚合仓位（Ticket=instId，无 comment 概念）——逐单 key 匹配在 CEX
// 结构性不存在，唯一正确粒度=venueKey+symbol+side 聚合量比对。
// 已知边界：同 symbol 两个仓同向腿的聚合量天然混算——per-position 归因不可能也不必，
// 告警粒度=键+贡献腿 position_id 集合（供人工定位）。OPENING 态仓不遍历腿（现状），
// 其 FILLED 腿存在窗口期聚合差，5min 周期内自然收敛。
package reconcile

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// cexAggKey 聚合键单点收口（R0-B2）：D-202③ 双侧归一 BareOf(v)——leg.Venue 存裸端点
// （"okx"，R1）而 querier VenueKey 是复合键（"okx:perp"），同 namespace 才可对拍；
// 两侧不同形=聚合静默全失配（本票实证病灶）。
func cexAggKey(venue, symbol, side string) string {
	return keydomain.BareOf(venue) + ":" + symbol + ":" + side
}

// cexAggTol 聚合量容差：0.5% 相对容差盖 lotSz/张数取整尘（不藏真分叉）；
// 1e-8 绝对下限=零值语义（0v0 与「0 vs 尘埃」判匹配）。同 key 同单位，永不跨量纲对拍（R0-B1 驳回留据）。
func cexAggTol(venue, local float64) float64 {
	return math.Max(1e-8, 0.005*math.Max(venue, local))
}

// appendPosID 去重追加贡献腿 position_id（告警归因列表，小集合线性查重）。
func appendPosID(ids []int64, id int64) []int64 {
	for _, x := range ids {
		if x == id {
			return ids
		}
	}
	return append(ids, id)
}

// reconcileCEXAggregate 对拍 cexLocal（本地 CEX 腿聚合）vs cexVenue（venue 持仓聚合），
// 返回本轮已确认（达连续确认阈值）的不一致数。三态：venue-only=系统外单不追踪（维持原语义，
// 外部/手工单不归本系统管）；差值≤tol=匹配静默；local>0 且超 tol（含 venue=0）须连续 3 轮确认
// 才 critical（D-401：venue 侧瞬读不告警；episode 内单发），告警含贡献腿 posIDs。
// D-401 失败读抑制：cexReadOK=本轮成功读取的 CEX venueKey 集——key 所属 venue 未读成功=未知
// 而非零，本轮跳过（不计 mismatches，pending 冻结），仅 warn 日志不进 alerts。
func (r *Reconciler) reconcileCEXAggregate(ctx context.Context, cexLocal, cexVenue map[string]float64, cexLocalPos map[string][]int64, cexReadOK map[string]bool) int {
	keys := make(map[string]bool, len(cexLocal)+len(cexVenue))
	for k := range cexLocal {
		keys[k] = true
	}
	for k := range cexVenue {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	// readOK 前缀扫（D-401 自审计钉死）：D-202 双侧归一 BareOf——agg key venue 段是裸端点
	// （"okx"），cexReadOK 键是复合 venueKey（"okx:perp"），同 namespace 经 keydomain 归一对拍。
	readOK := func(k string) bool {
		for vk := range cexReadOK {
			if keydomain.BareOf(vk) == keydomain.BareOf(k) {
				return true
			}
		}
		return false
	}

	mismatches := 0
	mismatchThisRound := make(map[string]bool) // 本轮处于不一致态的键（含 pending 观察），收尾清理用
	for _, k := range sorted {
		// key=venueKey:symbol:side（venueKey 本身含 ":" 如 okx:perp）——按首尾":"切三段。
		venue, symbol, side := k, "", ""
		if i := strings.Index(k, ":"); i >= 0 {
			if j := strings.LastIndex(k, ":"); j > i {
				venue, symbol, side = k[:i], k[i+1:j], k[j+1:]
			}
		}
		if !readOK(k) {
			log.Printf("[reconcile] CEX venue 查询失败/未读，key %s 本轮对拍跳过", k)
			continue
		}
		local, venueQty := cexLocal[k], cexVenue[k]
		switch {
		case local == 0 && venueQty > 0:
			// D-417 §1.4 降级实现：系统外单分支 log→warn 告警（同指纹 24h 冷却，宁可过警
			// 不漏警——收编漏检残余/误杀后敞口原本仅 log 隐身）。择优实现（近 24h 终态腿
			// 吻合才告警）需新 store 查询面>15 行，按 SSOT 降级条款取本方案。
			if last, ok := r.cexForeignWarned[k]; !ok || r.nowFn().Sub(last) > 24*time.Hour {
				r.cexForeignWarned[k] = r.nowFn()
				r.insertAlertCtx(ctx, "warn",
					fmt.Sprintf("venue %s 持仓 %s %s vol=%f 为系统外单（CEX 无 comment 源判别），不追踪——若为收编漏检残余请人工核查",
						venue, symbol, side, venueQty))
			}
			log.Printf("[reconcile] venue %s 持仓 %s %s vol=%f 为系统外单（CEX 无 comment 源判别），不追踪",
				venue, symbol, side, venueQty)
		case local > 0 && math.Abs(venueQty-local) <= cexAggTol(venueQty, local):
			// 匹配，静默
		default:
			mismatchThisRound[k] = true
			r.cexAggPending[k]++ // 无界递增；==3 恰一次发 critical（episode 单发），>3 不再计
			if r.cexAggPending[k] < 3 {
				log.Printf("[reconcile] CEX 聚合持仓对拍不一致（待确认 %d/3）venue=%s symbol=%s side=%s venue=%f local=%f posIDs=%v",
					r.cexAggPending[k], venue, symbol, side, venueQty, local, cexLocalPos[k])
				continue
			}
			if r.cexAggPending[k] == 3 {
				mismatches++
				msg := fmt.Sprintf("CEX 聚合持仓对拍不一致 venue=%s symbol=%s side=%s venue=%f local=%f posIDs=%v（人工定位）",
					venue, symbol, side, venueQty, local, cexLocalPos[k])
				r.insertAlertCtx(ctx, "critical", msg)
				log.Printf("[reconcile] %s", msg)
			}
		}
	}
	// D-401 收尾清理（自审计钉死）：按「本轮实际被评估且不再不一致」删——S3 跳过键=未评估，
	// 冻结保留（未知≠缓解也≠恶化）；readOK 前缀命中且未进 mismatch 集才 delete。
	for pk := range r.cexAggPending {
		if mismatchThisRound[pk] {
			continue
		}
		if readOK(pk) {
			delete(r.cexAggPending, pk)
		}
	}
	return mismatches
}
