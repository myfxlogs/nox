// earn_io.go —— earn 申购/赎回写侧划转记账（earn-io-ledger 票——残差对账盲区补账，D-539②血脉）（棘轮拆分件——
// ioTransfer+nowOr 从 exec.go 移出，零行为变）。
package dciexec

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// earnIOExtID 回执 venue id 抽键（earn-io 账面完整性专项——dedup 键域对齐）：
// 申购/赎回回执里 purchaseId/redeemId 与采集器 `se-<id>` 同键 → 双写撞键
// DO NOTHING 幂等不双记；回执无键=落 fallback（提案/产品 id 幂等键域，
// 采集面不回撞=残差另解释项，读侧剔除面同盖）。
func earnIOExtID(body, idKey, fallback string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err == nil {
		if raw, ok := m[idKey]; ok {
			var id string
			if json.Unmarshal(raw, &id) == nil && id != "" && id != "0" {
				return "se-" + id
			}
			var n json.Number
			if json.Unmarshal(raw, &n) == nil && n.String() != "" && n.String() != "0" {
				return "se-" + n.String()
			}
		}
	}
	return fallback
}

// ioTransfer earn 申购/赎回写侧划转记账——fin_venue_io direction=transfer
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
		Status:     "CONFIRMED",
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
