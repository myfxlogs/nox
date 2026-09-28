// binance_user_ws_balance.go —— D-542 S1：binance 私有流可用资金写口
// （events.go 超线析出：perp ACCOUNT_UPDATE balPut + spot outboundAccountPosition）。
package exchange

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"time"

	"arb/internal/store/pgstore"
)

// balPut D-542 S1：ACCOUNT_UPDATE → 活账本（available=wb−Σ持仓保证金，冻结无帧内字段不计，钳负为 0）。
// wb NaN=本帧无 USDT 余额行 → 不更账本（B/P 可独立出现，勿以 0 覆盖真值）。
func (b *BinanceUserWS) balPut(wb, iwSum float64, now time.Time) {
	if b.simulated || math.IsNaN(wb) {
		return
	}
	avail := wb - iwSum
	if avail < 0 {
		avail = 0
	}
	b.balLedger.Put(b.balPoolKey(), avail, wb, "USDT", "ws", now)
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
