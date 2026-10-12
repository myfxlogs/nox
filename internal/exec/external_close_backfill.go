// external_close_backfill.go —— D-015 ②（R1）：CEX 平仓帧兜底写 CLOSE EXTERNAL。
// 历史回填无命中（pnlCalibrator/cexCloseFill 双源皆空）时，查 cex_private_events 中
// 该 venue+contract+side、ts ≥ closing_started_at 的平仓帧——venue 侧自证「仓位已消失」
// 的权威推送（gate position_closes/liquidates/auto_deleverages、okx positions pos=0、
// binance ACCOUNT_UPDATE pa=0）。命中写 status=EXTERNAL（帧有价/费则取、无则 NULL 不造假）
// + writeCloseCashflows，「券商侧已无此腿」critical 不再发；仍无命中才走原告警出口。
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// externalCloseHit 平仓帧命中结果：px/feeUSD 帧有则取、无则 nil（不造假）。
type externalCloseHit struct {
	px     *float64
	feeUSD *float64
}

// externalCloseChannels 该 venue 的「position 消失/被平」权威帧频道（WS 落库审计源，
// 频道名=各私有 WS onAudit 写入值；MT venue 无 CEX 事件源返 nil）。
func externalCloseChannels(bare string) []string {
	switch bare {
	case "gate":
		return []string{"futures.position_closes", "futures.liquidates", "futures.auto_deleverages"}
	case "okx":
		return []string{"positions"}
	case "binance":
		return []string{"account"}
	}
	return nil
}

// externalCloseFallback 历史查证无命中后的兜底：查平仓帧证据 → 命中写 EXTERNAL 腿 +
// 现金流收尾（返 true）；无命中返 (false,nil) 由调用方发原告警；查询失败返 err（fail-closed）。
func (p *Pipeline) externalCloseFallback(ctx context.Context, pos *pgstore.Position, leg pgstore.PositionLeg, fx float64) (bool, error) {
	channels := externalCloseChannels(keydomain.BareOf(leg.Venue))
	if len(channels) == 0 {
		return false, nil
	}
	// 窗口=腿生命周期：平仓帧可先于 closing_started_at 到达（WS 先于本地 CLOSING
	// 标记/REST 回报——R1 竞态本体）；窗口内同 venue+contract+side 的平仓帧即本腿证据。
	since := time.Time{}
	if pos.OpenedAt != nil {
		since = *pos.OpenedAt
	}
	var hit *externalCloseHit
	for _, ch := range channels {
		rows, err := p.store.ListCexPrivateEventsSince(ctx,
			keydomain.BareOf(leg.Venue), keydomain.TypeOf(leg.Venue), ch, since)
		if err != nil {
			return false, fmt.Errorf("cex events %s: %w", ch, err)
		}
		for _, ev := range rows {
			if h := matchCloseFrame(leg, ch, ev.Payload); h != nil {
				hit = h // 最新覆盖最旧（ListCexPrivateEventsSince 已按 received_at 升序）
			}
		}
	}
	if hit == nil {
		return false, nil
	}
	// attempt 序位：可能已存在历史平仓尝试行（PENDING/FAILED 等非终态）——取现存最大+1，
	// 不抢占 attempt=1（该序位属权威 FILLED/回填路径）。
	legs, err := p.store.ListPositionLegs(ctx, pos.ID)
	if err != nil {
		return false, fmt.Errorf("list legs pos=%d: %w", pos.ID, err)
	}
	maxAttempt := int16(0)
	for _, l := range legs {
		if l.LegNo == leg.LegNo && l.Phase == "CLOSE" {
			if l.AttemptNo > maxAttempt {
				maxAttempt = l.AttemptNo
			}
			if l.Status == "FILLED" || l.Status == "EXTERNAL" {
				return true, nil // 终态已存在——幂等（并发重试/双路径竞合安全）
			}
		}
	}
	closeSide := "SELL"
	if leg.Side == "SELL" {
		closeSide = "BUY"
	}
	qty := leg.EffectiveQty()
	cl := &pgstore.PositionLeg{
		PositionID: pos.ID, LegNo: leg.LegNo, Phase: "CLOSE", AttemptNo: maxAttempt + 1,
		Venue: leg.Venue, Account: leg.Account, Symbol: leg.Symbol, Side: closeSide,
		Qty: qty, PriceExpected: leg.PriceExpected, PriceActual: hit.px,
		OrderID: leg.OrderID, Status: "EXTERNAL",
		FilledQty: &qty, AvgFillPrice: hit.px, FeeUSD: hit.feeUSD,
		SubmittedAt: ptrNow(), // D-208①：外部平回填腿同补
	}
	if hit.px != nil {
		cl.PriceExpected = *hit.px
	}
	if err := p.store.CreatePositionLeg(ctx, cl); err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
		return false, fmt.Errorf("external CLOSE leg pos=%d leg=%d: %w", pos.ID, leg.LegNo, err)
	}
	fee := 0.0
	if hit.feeUSD != nil {
		fee = *hit.feeUSD
	}
	if err := p.writeCloseCashflows(ctx, pos.ID, *cl, 0, 0, fee, fx); err != nil {
		return false, fmt.Errorf("external close cashflow pos=%d leg=%d: %w", pos.ID, leg.LegNo, err)
	}
	log.Printf("[exec] pos %d leg %d %s %s external close marked from venue frame", pos.ID, leg.LegNo, leg.Venue, leg.Symbol)
	return true, nil
}

// matchCloseFrame 单帧按 venue 帧 schema 匹配「该腿 position 已消失/被平」行。
// 帧 schema 即各 WS onAudit 落库原文：gate=结果行数组；okx positions={data:[]}；
// binance account={P:[]}（ACCOUNT_UPDATE 的 a 子帧 / outboundAccountPosition 全帧同键）。
func matchCloseFrame(leg pgstore.PositionLeg, channel string, payload []byte) *externalCloseHit {
	switch keydomain.BareOf(leg.Venue) {
	case "gate":
		return matchGateCloseRows(leg, payload)
	case "okx":
		var m struct {
			Data []map[string]json.RawMessage `json:"data"`
		}
		if json.Unmarshal(payload, &m) != nil {
			return nil
		}
		for _, it := range m.Data {
			if jstrRaw(it, "instId") != leg.Symbol || jstrRaw(it, "pos") != "0" {
				continue
			}
			if ps := strings.ToLower(jstrRaw(it, "posSide")); ps != "" && ps != "net" && !posSideMatchLeg(ps, leg.Side) {
				continue
			}
			return &externalCloseHit{} // positions 帧无成交价/费——NULL 不造假
		}
	case "binance":
		var m struct {
			P []map[string]json.RawMessage `json:"P"`
		}
		if json.Unmarshal(payload, &m) != nil {
			return nil
		}
		for _, it := range m.P {
			if jstrRaw(it, "s") != leg.Symbol || jstrRaw(it, "pa") != "0" {
				continue
			}
			if ps := strings.ToLower(jstrRaw(it, "ps")); ps != "" && ps != "both" && !posSideMatchLeg(ps, leg.Side) {
				continue
			}
			return &externalCloseHit{}
		}
	}
	return nil
}

// matchGateCloseRows gate 平仓域帧（position_closes/liquidates/auto_deleverages 同为
// 行数组）：contract 匹配 + side(long/short) 或 size 符号匹配方向；price/fee 帧有则取。
func matchGateCloseRows(leg pgstore.PositionLeg, payload []byte) *externalCloseHit {
	var rows []map[string]json.RawMessage
	if json.Unmarshal(payload, &rows) != nil {
		return nil
	}
	for _, it := range rows {
		if jstrRaw(it, "contract") != leg.Symbol {
			continue
		}
		side := strings.ToLower(jstrRaw(it, "side"))
		if side == "" {
			if sz, err := strconv.ParseFloat(jstrRaw(it, "size"), 64); err == nil && sz != 0 {
				if sz > 0 {
					side = "long"
				} else {
					side = "short"
				}
			}
		}
		if side != "" && !posSideMatchLeg(side, leg.Side) {
			continue
		}
		return &externalCloseHit{px: f64PtrRaw(it, "price"), feeUSD: f64PtrRaw(it, "fee")}
	}
	return nil
}

// posSideMatchLeg 仓位方向 ↔ OPEN 腿 side：long↔BUY / short↔SELL。
func posSideMatchLeg(posSide, legSide string) bool {
	return (posSide == "long" && legSide == "BUY") || (posSide == "short" && legSide == "SELL")
}

// jstrRaw map 行字符串字段提取（数字/字符串双形态宽容）。
func jstrRaw(m map[string]json.RawMessage, k string) string {
	raw, ok := m[k]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// f64PtrRaw map 行数值字段 → *float64（缺失/不可解析返 nil，不造假）。
func f64PtrRaw(m map[string]json.RawMessage, k string) *float64 {
	if s := jstrRaw(m, k); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return &f
		}
	}
	return nil
}
