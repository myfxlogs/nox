// permission.go —— D-188②：权限探测面。各所能力端点低频只读探活
// （复用已实证可达的只读端点，零下单零写径）：200→present、4xx→absent、传输层→unknown。
// 探测节奏与 cexscan 轮同频（scanOnce 挂点），结果写 fin_capability_permission。
package cexscan

import (
	"context"
	"log"
	"strings"
)

// permState 探测结果三态。
func permState(err error) (string, string) {
	if err == nil {
		return "present", ""
	}
	note := err.Error()
	if strings.Contains(note, "HTTP 4") {
		return "absent", note
	}
	return "unknown", note
}

// ProbePermissions 权限探活（EarnCollector 凭据/客户端复用）：
// dual_order=各所 DCI 产品列表只读端点；earn_subscribe=earn 采集端点。
// 只写探测结果表——凭据行读面零动。
func (s *Scanner) ProbePermissions(ctx context.Context) {
	c := s.earn
	probe := func(venue, perm string, err error) {
		state, note := permState(err)
		if uerr := s.store.UpsertCapabilityPermission(ctx, venue, perm, state, note); uerr != nil {
			log.Printf("[cexscan] perm upsert %s/%s: %v", venue, perm, uerr) // 不拖垮采集轮
		}
	}
	if c.bnKey != "" {
		_, err := c.collectBinanceDCI(ctx)
		probe("binance", "dual_order", err)
		_, err = c.CollectBinance(ctx)
		probe("binance", "earn_subscribe", err)
	}
	if c.okxKey != "" {
		_, err := c.collectOKXDCI(ctx)
		probe("okx", "dual_order", err)
		_, err = c.CollectOKX(ctx)
		probe("okx", "earn_subscribe", err)
	}
	// gate dual 公开端点（D-183 实拍 200）——凭据非前置条件。
	_, gerr := c.collectGateDCI(ctx)
	probe("gate", "dual_order", gerr)
}
