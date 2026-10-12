// D-282 pgstore.Store → notify.PriceAlertStore 适配器。
package notify

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// PgStorePriceAlertAdapter 包装 pgstore.Store 实现 notify.PriceAlertStore 接口。
type PgStorePriceAlertAdapter struct {
	Store *pgstore.Store
}

// ListEnabledPriceAlerts 转换 pgstore.PriceAlertRule → notify.PriceAlertRule。
func (a *PgStorePriceAlertAdapter) ListEnabledPriceAlerts(ctx context.Context) ([]PriceAlertRule, error) {
	rows, err := a.Store.ListEnabledPriceAlerts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PriceAlertRule, len(rows))
	for i, r := range rows {
		out[i] = PriceAlertRule{
			ID: r.ID, Symbol: r.Symbol, Op: r.Op, Threshold: r.Threshold,
			Level: r.Level, Message: r.Message, LastAlertAt: r.LastAlertAt,
		}
	}
	return out, nil
}

// UpdatePriceAlertLastAlert 透传。
func (a *PgStorePriceAlertAdapter) UpdatePriceAlertLastAlert(ctx context.Context, id int64, at time.Time) error {
	return a.Store.UpdatePriceAlertLastAlert(ctx, id, at)
}

// compile-time: *PgStorePriceAlertAdapter 实现 PriceAlertStore 接口。
var _ PriceAlertStore = (*PgStorePriceAlertAdapter)(nil)
