// D-271 S3 账户扫描：读 AccountMonitor 缓存（接口注入，不导入 dashboard）。
package cexscan

import (
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
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
	Ts         time.Time
}

// IsRealAccount D-360 S1：real 判定纯函数（证真才留）。
// cex → !Simulated（原生键，v2 核证推翻「构造假设」）；cfd → venue∈broker_accounts.is_real
// 键集（核证点②：snapshot.venue ≡ user_id::text，生产 DB 两列互证留证 9-20）；
// 未知 AssetType → 丢。realKeys nil 时 cfd 一律丢（fail-closed）。
func IsRealAccount(assetType string, simulated bool, venue string, realKeys map[string]bool) bool {
	switch assetType {
	case "cex":
		return !simulated
	case "cfd":
		return realKeys[venue]
	default:
		return false
	}
}

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
			Venue:      info.Venue,
			VenueType:  info.VenueType,
			Balance:    info.Balance,
			Equity:     info.Equity,
			FreeMargin: info.FreeMargin,
			Currency:   info.Currency,
			AssetType:  info.AssetType,
			Simulated:  info.Simulated,
			Ts:         now,
		})
	}
	return out
}
