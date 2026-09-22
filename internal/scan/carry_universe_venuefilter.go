package scan

// D-388 S1.3：发现层 venue status 过滤（从 carry_universe.go 拆出控行数）。

import (
	"context"
	"log"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// activeVenueStatuses 拉 venue→status；查询失败 fail-open 返回 nil（视同全放行，
// 同 productGroupMap/trade_mode 惯例——发现层不因状态源故障误杀全部候选）。
func (sc *CarryUniverseScanner) activeVenueStatuses(ctx context.Context) map[string]string {
	statuses, err := sc.store.ListBrokerAccountStatuses(ctx)
	if err != nil {
		log.Printf("[scan] carry universe: broker statuses: %v（fail-open 放行）", err)
		return nil
	}
	return statuses
}

// keepDiscoverableFacts 丢弃非 connected broker 托管 venue 的 facts（停用即退出配对，
// 不等 facts 自然过期）。谓词=pgstore.BrokerVenueDiscoverable（autotrade 同收口，禁复制逻辑）。
func keepDiscoverableFacts(fs []fact.Fact, statuses map[string]string) []fact.Fact {
	out := fs[:0]
	for _, f := range fs {
		if pgstore.BrokerVenueDiscoverable(statuses, f.Venue) {
			out = append(out, f)
		}
	}
	return out
}
