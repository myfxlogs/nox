// account_monitor_proto.go D-107 P2 拆分：accountToProto + accountFingerprint（从 account_monitor.go 移出，纯移动零逻辑改）。
package dashboard

import (
	"strconv"
	"strings"
	"time"

	"arb/internal/reconcile"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// accountToProto 账户视图 → proto（D-053）。
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
		UpdatedAt:  timestamppb.New(now),
	}
	return pb
}

// accountFingerprint 视图指纹：任一账户的余额/净值/占用/可用/信用/浮盈/在线状态变化即触发广播。
// 权益实时跳变 → 值变了就推（5s 节奏，全量快照量小，可接受）。
func accountFingerprint(view []*v1.AccountInfo) string {
	var sb strings.Builder
	for _, a := range view {
		sb.WriteString(a.VenueKey)
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Balance, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Equity, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Margin, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.FreeMargin, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Credit, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Profit, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatBool(a.Online))
		sb.WriteString(";")
	}
	return sb.String()
}
