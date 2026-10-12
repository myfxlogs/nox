// permission.go —— D-188②：权限探测面落库（零 HTTP——吃 Collect/CollectDCI 同轮
// per-venue 错误面写 fin_capability_permission；票面返修①：禁重调端点防 API 翻倍 429）。
// 三态：无错→present、HTTP 4xx→absent、传输层/凭据缺→unknown。
package cexscan

import (
	"context"
	"log"
	"strings"
)

// permState 错误面→探测态。
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

// RecordPermissions 采集错误面→权限行（dual_order=DCI 端点、earn_subscribe=earn 端点；
// venue 行未调=凭据缺无该 venue 键→不造行——凭据缺口已由 credential 条件面承载）。
func (s *Scanner) RecordPermissions(ctx context.Context, earnErrs, dciErrs map[string]error) {
	rec := func(venue, perm string, err error) {
		state, note := permState(err)
		if uerr := s.store.UpsertCapabilityPermission(ctx, venue, perm, state, note); uerr != nil {
			log.Printf("[cexscan] perm upsert %s/%s: %v", venue, perm, uerr)
		}
	}
	for v, err := range dciErrs {
		rec(v, "dual_order", err)
	}
	for _, v := range []string{"binance", "okx", "gate"} { // 成功 venue 无 errs 键→补 present
		if _, bad := dciErrs[v]; !bad {
			rec(v, "dual_order", nil)
		}
	}
	for v, err := range earnErrs {
		rec(v, "earn_subscribe", err)
	}
	for _, v := range []string{"binance", "okx"} { // gate 无 earn 采集端点不补
		if _, bad := earnErrs[v]; !bad {
			rec(v, "earn_subscribe", nil)
		}
	}
}
