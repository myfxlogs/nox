// reject_class.go —— D-560 S3：拒单签名分类 + 熔断快照共享件（autotrade 写侧/
// dashboard 读侧共调，P3 单源；dashboard→autotrade 无白名单边故落 errclass）。
package errclass

import (
	"strings"
	"time"
)

// RejectClass 拒单类别（熔断签名段；前端白话映射在渲染层）。
const (
	RejectVariant = "variant"      // 报价变体占槽/stale-quote（tick 级自愈类）
	RejectFunding = "funding"      // 资金不足（暂停待确认+2h 兜底）
	RejectLiq     = "liq-distance" // 强平距离闸
	RejectOther   = "other"        // 未知类别（默认 30min）
)

// ClassifyReject 拒文 → 熔断类别。liq-distance 先于 funding 判（其本身命中
// IsFundingRejectMsg，但熔断分级不同）；variant/stale-quote 同桶 5min 窗。
func ClassifyReject(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "variant") || strings.Contains(low, "stale"):
		return RejectVariant
	case strings.Contains(low, "liq-distance"):
		return RejectLiq
	case IsFundingRejectMsg(msg):
		return RejectFunding
	default:
		return RejectOther
	}
}

// RejectBreakerInfo 熔断器快照行（autotrade.Gate 内存态透出；dashboard 只读渲染）。
type RejectBreakerInfo struct {
	Class    string    // RejectClass 之一
	Count    int       // 本熔断窗累计抑制数（PAUSED 行 ×N）
	Paused   bool      // funding 类=暂停待确认（ResumeXAssetCombo 或 2h 兜底）
	ResumeAt time.Time // 窗口到点/兜底自恢复时刻
}
