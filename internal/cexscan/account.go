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
	Ts         time.Time
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
			Ts:         now,
		})
	}
	return out
}
