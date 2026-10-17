// earn_io.go —— D-596：earn 申购/赎回写侧划转记账（棘轮拆分件——
// ioTransfer+nowOr 从 exec.go 移出，零行为变）。
package dciexec

import (
	"context"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// ioTransfer D-596：earn 申购/赎回写侧划转记账——fin_venue_io direction=transfer
// 行（from_wallet→to_wallet；venue asset/transfer 端点枚举不含 earn 钱包对=
// 采集盲区，写侧成功径自记）。external_id=提案/productId 幂等键（重试不双记）。
// best-effort 不阻塞执行链——读败如实 log 不翻执行结果。
func (e *Executor) ioTransfer(ctx context.Context, venue, ccy string, amount float64,
	fromWallet, toWallet, externalID, receipt string) {
	if venue == "" || amount <= 0 {
		return
	}
	n, err := e.Store.InsertVenueIO(ctx, []pgstore.VenueIO{{
		Venue:      venue,
		Direction:  "transfer",
		Ccy:        ccy,
		Amount:     amount,
		Status:     "exec",
		ExternalID: externalID,
		Ts:         e.nowOr(),
		FromWallet: fromWallet,
		ToWallet:   toWallet,
	}})
	if err != nil {
		log.Printf("[dciexec] earn io 划转记账败 venue=%s %s→%s %.8f: %v", venue, fromWallet, toWallet, amount, err)
		return
	}
	if n == 0 {
		log.Printf("[dciexec] earn io 幂等命中（重试不双记） venue=%s %s", venue, externalID)
	}
}

func (e *Executor) nowOr() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
