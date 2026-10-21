// nav_equity_available_test.go —— NAV 读败显缺锚（总览瓦片审计①）：
// ComputeNav 读败 → equity_available=false（断供≠零，前端显「—」非 $0.00）；
// 读通 → true 且数值照常。computeNavFn seam 注入，零 DB 依赖。
package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func TestNavEquityAvailable_ReadFail(t *testing.T) {
	s := &Service{computeNavFn: func(context.Context, time.Time) (*pgstore.NavReport, error) {
		return nil, errors.New("pg conn refused")
	}}
	total, _, byCcy, _, ok := s.equityTotalUSD(context.Background())
	if ok {
		t.Fatalf("读败应 available=false，got true（total=%v）", total)
	}
	if total != 0 || byCcy != nil {
		t.Fatalf("读败应零值+nil 分账，got total=%v byCcy=%v", total, byCcy)
	}
}

func TestNavEquityAvailable_ReadOK(t *testing.T) {
	s := &Service{computeNavFn: func(context.Context, time.Time) (*pgstore.NavReport, error) {
		return &pgstore.NavReport{ByCcy: map[string]pgstore.NavBucket{
			"USD":  {Nav: 100},
			"USDT": {Nav: 50.5},
		}}, nil
	}}
	total, _, byCcy, _, ok := s.equityTotalUSD(context.Background())
	if !ok {
		t.Fatal("读通应 available=true")
	}
	if total != 150.5 || byCcy["USD"] != 100 {
		t.Fatalf("NAV 合错: total=%v byCcy=%v", total, byCcy)
	}
}
