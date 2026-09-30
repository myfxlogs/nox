// D-271 S3 账户扫描：读 AccountMonitor 缓存（接口注入，不导入 dashboard）。
package cexscan

import (
	"time"

	v1 "arb/internal/gen/dashboardv1"
)

// AccountSnapshotter 接口——main.go 注入 *dashboard.AccountMonitor（实现 Snapshot()）。
// 不导入 dashboard 包，避免逆向依赖；只用 proto 生成的 v1.AccountInfo 类型。
type AccountSnapshotter interface {
	Snapshot() []*v1.AccountInfo
}

// AccountSnapshot 账户快照（从 v1.AccountInfo 映射）。
type AccountSnapshot struct {
	Venue      string
	VenueType  string
	Balance    float64
	Equity     float64
	FreeMargin float64
	Currency   string // D-358：原币（MT 原币直传/CEX USDT，核证见 migration 0111 头注）
	AssetType  string // D-360：cfd / cex（核证点①，reconcile_queriers.go:80 值域两值）
	Simulated  bool   // D-360：CEX 模拟盘标记（D-156 V2 既有，映射曾丢弃）
	// BufferMultiple D-405 S2：CEX 距强平缓冲倍数=equity/MMR（<=0=未采）。
	BufferMultiple float64
	Ts             time.Time
}

// IsRealAccount 已随 3d-P1#1c 下沉 internal/venue（venue.IsRealAccount；写侧归一
// venue.NormalizeVenueType 同包）——cexscan 写侧与 dashboard 实时态读侧共用单源。

// ScanAccounts 调 s.Snapshot()，转 *v1.AccountInfo → AccountSnapshot。
func ScanAccounts(s AccountSnapshotter) []AccountSnapshot {
	infos := s.Snapshot()
	out := make([]AccountSnapshot, 0, len(infos))
	now := time.Now()
	for _, info := range infos {
		if info == nil {
			continue
		}
		out = append(out, AccountSnapshot{
			Venue:          info.Venue,
			VenueType:      info.VenueType,
			Balance:        info.Balance,
			Equity:         info.Equity,
			FreeMargin:     info.FreeMargin,
			Currency:       info.Currency,
			AssetType:      info.AssetType,
			Simulated:      info.Simulated,
			BufferMultiple: info.BufferMultiple,
			Ts:             now,
		})
	}
	return out
}
