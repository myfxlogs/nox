// binance_user_ws_balance.go —— D-542 S1：binance 私有流可用资金写口
// （events.go 超线析出：perp ACCOUNT_UPDATE balPut + spot outboundAccountPosition）。
package exchange

import (
	"arb/internal/bus"
	"context"
	"encoding/json"
	"log"
	"math"
	"time"

	"arb/internal/store/pgstore"
)

// balPut D-542 S1：ACCOUNT_UPDATE → 活账本。D-142：equity=wb+ΣUP（合约权益含未实现盈亏），
// available=eq−Σiw（持仓保证金扣除不变），冻结无帧内字段不计，钳负为 0。
// wb NaN=本帧无 USDT 余额行 → 不更账本（B/P 可独立出现，勿以 0 覆盖真值）。
func (b *BinanceUserWS) balPut(wb, upSum, iwSum float64, now time.Time) {
	if b.simulated || math.IsNaN(wb) {
		return
	}
	eq := wb + upSum // D-142：权益=钱包余额+未实现盈亏（与 REST totalMarginBalance 同口径）
	avail := eq - iwSum
	if avail < 0 {
		avail = 0
	}
	b.balLedger.Put(b.balPoolKey(), avail, eq, "USDT", "ws", now)
}

// onSpotAccountPosition outboundAccountPosition → B[{a,f,l}] USDT free+locked 落快照。
// spot 无持仓概念——持仓缓存不动（订单侧由 executionReport 覆盖）。
// D-542：同帧喂活账本（spot available=free）。
func (b *BinanceUserWS) onSpotAccountPosition(ctx context.Context, data []byte) {
	var acct struct {
		B []struct {
			A string `json:"a"`
			F string `json:"f"`
			L string `json:"l"`
		} `json:"B"`
	}
	if err := json.Unmarshal(data, &acct); err != nil {
		return
	}
	if b.simulated {
		return
	}
	// D-192：全资产落活缓存（现货域边界——dciexec 余额读流主源）。
	var rows []struct{ A, F, L string }
	for _, bal := range acct.B {
		rows = append(rows, struct{ A, F, L string }{bal.A, bal.F, bal.L})
	}
	bus.SpotAssets.PutAll(b.balPoolKey(), rows, b.now())
	for _, bal := range acct.B {
		if bal.A != "USDT" {
			continue
		}
		free, perr := f64z(bal.F)
		if perr != nil {
			log.Printf("[binance-ws] drop field F: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		locked, perr := f64z(bal.L)
		if perr != nil {
			log.Printf("[binance-ws] drop field L: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		eq := free + locked
		b.balLedger.Put(b.balPoolKey(), free, eq, "USDT", "ws", b.now()) // D-542 S1：spot available=free
		row := pgstore.VenueAccountSnapshot{
			Venue: "binance", VenueType: b.venueType,
			BalanceRaw: eq, EquityRaw: eq,
			Currency: "USDT", FetchedAt: b.now(), Src: "binance_event",
		}
		if err := b.sink.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
			log.Printf("[binance-user-ws] insert spot snapshot: %v", err)
		}
	}
	WarnUnconsumed("binance", "outboundAccountPosition", data, binanceSpotAcctPosConsumed)
}
