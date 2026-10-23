// wire_restpoll_pause.go —— venue REST 采集暂停开关注入件（app_config
// NOX_REST_POLL_PAUSED_<VENUE>=1 停；仅 REST 拍面——WS/earn/持仓零动）。
package main

import (
	"context"
	"strings"

	"nox/internal/store/pgstore"
)

func restPollPausedFn(st *pgstore.Store) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, venue string) (bool, error) {
		v, ok, err := st.AppConfigInt64(ctx, "NOX_REST_POLL_PAUSED_"+strings.ToUpper(venue))
		if err != nil {
			return false, err
		}
		return ok && v == 1, nil
	}
}

// marketWsPausedFn 行情 WS 停止闸读键件（NOX_MARKET_WS_PAUSED_<VENUE>=1 停——
// 正键域独立于 REST 暂停键；读败 fail-open 不闸）。
func marketWsPausedFn(st *pgstore.Store) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, venue string) (bool, error) {
		v, ok, err := st.AppConfigInt64(ctx, "NOX_MARKET_WS_PAUSED_"+strings.ToUpper(venue))
		if err != nil {
			return false, err
		}
		return ok && v == 1, nil
	}
}

// okxRestFamilyOnFn okx REST 实名豁免族闸装配（NOX_OKX_REST_<FAMILY>=1 开）。
func okxRestFamilyOnFn(st *pgstore.Store) func(context.Context, string) bool {
	return func(ctx context.Context, family string) bool {
		v, ok, err := st.AppConfigInt64(ctx, "NOX_OKX_REST_"+strings.ToUpper(family))
		return err == nil && ok && v == 1 // 读败=关闸 fail-closed
	}
}
