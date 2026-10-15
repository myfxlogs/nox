// binance_user_ws_balance.go —— D-542 S1：binance 私有流可用资金写口
// （events.go 超线析出：perp ACCOUNT_UPDATE balPut + spot outboundAccountPosition）。
package exchange

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"nox/internal/data/bus"
	"strings"
	"time"

	"nox/internal/store/pgstore"
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
// 同帧喂活账本（spot available=free）。
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
	// 全资产落活缓存（现货域边界——dciexec 余额读流主源）。
	var rows []struct{ A, F, L string }
	for _, bal := range acct.B {
		rows = append(rows, struct{ A, F, L string }{bal.A, bal.F, bal.L})
	}
	bus.SpotAssets.PutAll(b.balPoolKey(), rows, b.now())
	// D-528：分币行（非 LD* 且 qty>0）随帧同写——现货非稳定币持仓入快照/NAV；
	// LD* 前缀=earn 镜像（earn 池行已记账，入则双计）；qty=0 资产不写
	// （零清行由 REST 校准径 prev 并集补零——帧不写零行防 ~90 空资产行洪）。
	var snapRows []pgstore.VenueAccountSnapshot
	for _, bal := range acct.B {
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
		if bal.A == "USDT" {
			b.balLedger.Put(b.balPoolKey(), free, eq, "USDT", "ws", b.now()) // D-542 S1：spot available=free
		}
		if isLDEchoAsset(bal.A) || eq <= 0 {
			continue
		}
		snapRows = append(snapRows, pgstore.VenueAccountSnapshot{
			Venue: "binance", VenueType: b.venueType,
			BalanceRaw: eq, EquityRaw: eq, FreeMarginRaw: free,
			Currency: bal.A, FetchedAt: b.now(), Src: "binance_event",
		})
	}
	if len(snapRows) > 0 {
		if err := b.sink.InsertVenueAccountSnapshots(ctx, snapRows); err != nil {
			log.Printf("[binance-user-ws] insert spot snapshot: %v", err)
		}
	}
	WarnUnconsumed("binance", "outboundAccountPosition", data, binanceSpotAcctPosConsumed)
}

// isLDEchoAsset binance 现货 LD* earn 镜像判定（与 reconcile.isBinanceLDEcho
// 同口径——包间不共享，两文件各留一行注释互指）：
// LD 前缀+尾段=earn 持仓现货回显（earn 池行已记账，入快照=双计）；
// LDO=Lido DAO 真代币白名单（其 earn 镜像为 LDLDO）。
func isLDEchoAsset(asset string) bool {
	if asset == "LDO" {
		return false
	}
	return strings.HasPrefix(asset, "LD") && len(asset) > 2
}
