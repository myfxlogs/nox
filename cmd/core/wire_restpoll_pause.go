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
