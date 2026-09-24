// account_info_map.go —— MT4/MT5 AccountSummary → AccountInfo 映射（D-053；D-448 自 reconcile_queriers.go 拆出压线）。
package reconcile

import (
	"strings"

	"arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// accountSummaryToInfo 从 MT5 AccountSummary 映射账户视图（D-053）。
func accountSummaryToInfoMT5(venue, venueType string, sum *mt5pb.AccountSummary) *AccountInfo {
	return &AccountInfo{
		VenueKey:   venue + ":" + venueType,
		Venue:      venue,
		VenueType:  venueType,
		AssetType:  "cfd",
		Balance:    sum.GetBalance(),
		Credit:     sum.GetCredit(),
		Profit:     sum.GetProfit(),
		Equity:     sum.GetEquity(),
		Margin:     sum.GetMargin(),
		FreeMargin: sum.GetFreeMargin(),
		Leverage:   sum.GetLeverage(),
		Currency:   sum.GetCurrency(),
		Simulated:  mt5AcctSimulated(sum.GetType()), // D-448 S1
	}
}

// mt5AcctSimulated D-448：MT5 AccountSummary.Type（string，实测 demo→"demo"/real→"real"，
// 2026-09-24 Exness 277842155/3001157 实证）映射虚拟盘族：demo/contest→true，空串/其他→false。
func mt5AcctSimulated(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, "demo") || strings.Contains(t, "contest")
}

// accountSummaryToInfoMT4 从 MT4 AccountSummary 映射账户视图（字段与 MT5 同形，生成类型不同）。
func accountSummaryToInfoMT4(venue, venueType string, sum *mt4.AccountSummary) *AccountInfo {
	return &AccountInfo{
		VenueKey:   venue + ":" + venueType,
		Venue:      venue,
		VenueType:  venueType,
		AssetType:  "cfd",
		Balance:    sum.GetBalance(),
		Credit:     sum.GetCredit(),
		Profit:     sum.GetProfit(),
		Equity:     sum.GetEquity(),
		Margin:     sum.GetMargin(),
		FreeMargin: sum.GetFreeMargin(),
		Leverage:   sum.GetLeverage(),
		Currency:   sum.GetCurrency(),
		Simulated: sum.GetType() == mt4.AccountType_AccountType_Demo ||
			sum.GetType() == mt4.AccountType_AccountType_Contest, // D-448 S1（枚举精确比较）
	}
}
