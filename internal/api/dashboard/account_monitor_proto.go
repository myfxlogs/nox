// account_monitor_proto.go D-107 P2 拆分：accountToProto + accountFingerprint（从 account_monitor.go 移出，纯移动零逻辑改）。
package dashboard

import (
	"time"

	"arb/internal/ledger/reconcile"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "arb/internal/gen/dashboardv1"
)

// accountToProto 账户视图 → proto（D-053）。
// D-362 S4④ 核证：VenueKey 字段随 querier 生效键（撞键非赢家带 #id 后缀）→ Accounts 页
// 双行可分；Venue/VenueType 数据字段保持原值（binance/perp）+Simulated 徽标区分可读。
func accountToProto(a *reconcile.AccountInfo, online bool, now time.Time) *v1.AccountInfo {
	pb := &v1.AccountInfo{
		VenueKey:   a.VenueKey,
		Venue:      a.Venue,
		VenueType:  a.VenueType,
		AssetType:  a.AssetType,
		Balance:    a.Balance,
		Equity:     a.Equity,
		Margin:     a.Margin,
		FreeMargin: a.FreeMargin,
		Credit:     a.Credit,
		Profit:     a.Profit,
		Leverage:   a.Leverage,
		Currency:   a.Currency,
		Online:     online,
		Simulated:  a.Simulated,
		Website:    a.Website,
		UpdatedAt:  timestamppb.New(now),
		Holdings:   a.Holdings,
		// D-405 S2：CEX 距强平缓冲倍数（写时归一 equity/MMR；0=未采）
		BufferMultiple: a.BufferMultiple,
	}
	return pb
}
