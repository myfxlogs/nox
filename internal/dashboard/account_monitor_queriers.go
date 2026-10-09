// account_monitor_queriers.go —— CEX 查询器装配面（D-133 拆分归纪，>300 行门禁）：
// exchange_configs → realOnlyConfigs → 撞键归一 → querier 构建/缓存 + 配置删除幽灵行淘汰。
package dashboard

import (
	"context"
	"log"
	"strings"
	"time"

	"arb/internal/reconcile"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
)

// binanceProxyBase D-145 env 收口：BINANCE_PROXY_URL 缺省回退值由装配层注入
// （cfg 产物，boot 单次；空=fapi 直连语义不变）。
var binanceProxyBase string

// SetBinanceProxyBase boot 注入 binance 缺省 base（先于一切 querier 装配调用）。
func SetBinanceProxyBase(v string) { binanceProxyBase = v }

// resolveExchangeBaseURL 推导 CEX 交易所 base（D-059 抽公共，账户查询与执行器共用）。
// 行为与原 queriers() 内联一致：base_url 显式 → 用之；空 → binance 默认注入代理基址
// （无则 fapi 直连，451 由部署负责）、okx 默认 www.okx.com；binance 代理 base 是 fapi 风格，
// 无 /api/ 段则 fapi→api 得 spot base（与采集一致）。
func resolveExchangeBaseURL(c pgstore.ExchangeConfig) string {
	base := c.BaseURL
	if base == "" {
		switch c.Venue {
		case "binance":
			base = binanceProxyBase
			if base == "" {
				base = "https://fapi.binance.com" // 无代理直连；451 由部署负责
			}
		case "okx":
			base = "https://www.okx.com" // 实盘域名
		}
	}
	if c.Venue == "binance" && c.VenueType == "spot" && !strings.Contains(base, "/api/") {
		// 与采集一致：仅 spot 需从 fapi 风格代理 base 推导 spot base。
		// naive replace 按首命中换段——仅 spot 走此推导（perp base 含 fapi 段原样保留，
		// 历史 demo 形前缀同样不被错改；D-516 起 sim 行上游 fail-closed 不入此路径）。
		base = strings.Replace(base, "fapi", "api", 1)
	}
	return base
}

func (m *AccountMonitor) queriers(ctx context.Context) []reconcile.AccountQuerier {
	qs := m.bm.AccountQueriers()
	m.mu.RLock()
	exchs := m.exchs
	m.mu.RUnlock()
	if exchs != nil {
		cfgs, err := exchs.ListExchangeConfigs(ctx)
		if err != nil {
			log.Printf("[account-mon] list exchanges: %v", err)
		} else {
			// D-362 S2/R1：撞键归一（赢家 canonical/输家 #id，与 executor/持仓同源）。
			// D-516：sim 行先于归一剔除——不占 canonical、不建查询器。
			m.mu.Lock()
			live := realOnlyConfigs(cfgs, &m.simWarned, "account")
			h := exchConfigsHash(live)
			if h == m.qCacheHash && m.qCache != nil {
				qs = append(qs, m.qCache...)
				m.mu.Unlock()
			} else {
				m.mu.Unlock()
				// D-362 S2/R1：撞键归一（赢家 canonical/输家 #id，与 executor/持仓同源）。
				winners := cexKeyWinners(live)
				var built []reconcile.AccountQuerier
				for i := range live {
					c := &live[i]
					base := resolveExchangeBaseURL(*c)
					q := reconcile.NewCEXAccountQuerier(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Website, c.Simulated)
					q.SetKeyOverride(reconcile.CexVenueKey(c.Venue, c.VenueType, c.ID, winners))
					built = append(built, q)
					log.Printf("[account] cex querier: %s:%s", c.Venue, c.VenueType)
				}
				cexQuerierBuildTotal.Add(1)
				m.mu.Lock()
				removed := m.pruneRemovedCexKeysLocked(built)
				m.qCacheHash, m.qCache = h, built
				m.mu.Unlock()
				if m.evTopic != nil {
					for _, ev := range removed {
						m.evTopic.Publish(ev)
					}
				}
				qs = append(qs, built...)
			}
		}
	}
	m.mu.RLock()
	qs = append(qs, m.statics...)
	m.mu.RUnlock()
	return qs
}

// pruneRemovedCexKeysLocked D-133 域2 幽灵行修复（mu 持锁前提，返回待发布 offline 事件）。
// exchange_configs 删行 → 哈希变 → querier 重建，但 states/cur 原本无淘汰 → 被删账户残留
// 活快照直到重启（binance:earn 生产实测残留 9321.73 幽灵值）。哈希变更点按新 CEX 键集做
// 差集淘汰：旧 qCache 键不在新集 → 拆除全部关联态（states/cur/dirty/stream*/kick 防抖/
// pendingKick），并向事件流补发一条 online=false 末态 upsert——既有 AccountEvent proto
// 仅 upsert 单形态无 remove（stream.proto:56 注释），离线标+回放源摘除=新连接绝迹、
// 已连接端行标离线诚实态。
func (m *AccountMonitor) pruneRemovedCexKeysLocked(newQS []reconcile.AccountQuerier) []*v1.AccountEvent {
	keep := make(map[string]bool, len(newQS))
	for _, q := range newQS {
		keep[q.VenueKey()] = true
	}
	var evs []*v1.AccountEvent
	for _, q := range m.qCache {
		vk := q.VenueKey()
		if keep[vk] {
			continue
		}
		if st := m.states[vk]; st.acct != nil {
			asOf := st.asOf
			if asOf.IsZero() {
				asOf = time.Now()
			}
			evs = append(evs, &v1.AccountEvent{Account: accountToProto(st.acct, false, asOf)})
		}
		delete(m.states, vk)
		delete(m.cur, vk)
		delete(m.dirty, vk)
		delete(m.streamKnown, vk)
		delete(m.streamOnline, vk)
		delete(m.pendingKick, vk)
		delete(m.frameKickAt, vk)
		delete(m.ledgerKickAt, vk)
		log.Printf("[account-mon] %s removed: 配置行删除→账户行摘除（querier 键集淘汰）", vk)
	}
	return evs
}
