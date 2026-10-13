// pit.go —— D-255：venue 坑表消费辅件（fix 参数映射+抑单窗文案）。
package dciexec

import (
	"encoding/json"

	"nox/internal/store/pgstore"
)

// applyRejectFix 坑表 fix 动作参数映射（委员会种行——自动落坑不写 fix）。
// 现支持 deposit_decimals（如 -9000「0 decimals」整数要求坑→depositAmount
// floor 到整数）；未知键跳（fail-open——参数面不改不如实行的映射）。
func applyRejectFix(deposit float64, params []byte) float64 {
	var m map[string]any
	if len(params) == 0 || json.Unmarshal(params, &m) != nil {
		return deposit
	}
	if v, ok := m["deposit_decimals"].(float64); ok {
		return floorTo(deposit, int(v))
	}
	return deposit
}

// pitExpiry 抑单窗到期文案（expires 空=永久坑——委员会手置）。
func pitExpiry(p *pgstore.VenueReject) string {
	if p.ExpiresAt == nil {
		return "永久"
	}
	return p.ExpiresAt.Format("01-02 15:04")
}
