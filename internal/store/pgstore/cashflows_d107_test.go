package pgstore

import (
	"context"
	"testing"
)

// d107CF 复用 d438CF（position_cashflows 估计行）；d107Income 复用 d438Income（income_events）。
// 本文件锚=PositionNetUSD funding/swap 分量真值化（D-107 与 D-438 结算轨规则统一）。

// TestD107_IncomeReplacesEstimate #509 型实拍形态：funding 估计两行 1.4022+1.7773
// vs income 实结三笔 0.3313+0.5121+0.6116 → 分量=Σincome=1.4650（非估计和 3.1795）；
// PositionNetUSD（override 价差=0、零 commission）同值；估计行 quality 标记不回写。
func TestD107_IncomeReplacesEstimate(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	d438CF(t, s, posID, "funding", 1.4022, "estimated")
	d438CF(t, s, posID, "funding", 1.7773, "estimated")
	d438Income(t, s, posID, 0.3313)
	d438Income(t, s, posID, 0.5121)
	d438Income(t, s, posID, 0.6116)

	got, err := s.fundingSwapNetUSD(ctx, posID)
	if err != nil {
		t.Fatal(err)
	}
	if got < 1.4549 || got > 1.4551 {
		t.Fatalf("funding 分量应=Σincome_events 1.4550, got %v", got)
	}
	zero := 0.0
	net, err := s.PositionNetUSD(ctx, posID, &zero)
	if err != nil {
		t.Fatal(err)
	}
	if net < 1.4549 || net > 1.4551 {
		t.Fatalf("PositionNetUSD 应=1.4550（override+实结+零佣金）, got %v", net)
	}
	// quality 标记不回写：估计行保持 estimated（取值口径变，行面零改写）。
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM position_cashflows WHERE position_id=$1 AND kind='funding' AND quality='estimated'`,
		posID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("估计行应原样保留 2 行 estimated, got %d", n)
	}
}

// TestD107_NoIncomeFallback 无 income 仓：回退估计 SUM 不变（含 swap 行，legacy 形态保持）。
func TestD107_NoIncomeFallback(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	d438CF(t, s, posID, "funding", 1.4022, "estimated")
	d438CF(t, s, posID, "funding", 1.7773, "estimated")
	d438CF(t, s, posID, "swap", 0.5, "estimated")

	got, err := s.fundingSwapNetUSD(ctx, posID)
	if err != nil {
		t.Fatal(err)
	}
	if got < 3.6794 || got > 3.6796 {
		t.Fatalf("无 income 应回退估计 SUM 3.6795, got %v", got)
	}
}

// TestD107_SwapNotReplaced income 存在时仅 funding 被替换——swap 行（MT 息差）保持计入。
func TestD107_SwapNotReplaced(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	d438CF(t, s, posID, "funding", 3.18, "estimated")
	d438CF(t, s, posID, "swap", 0.5, "estimated")
	d438Income(t, s, posID, 1.46)

	got, err := s.fundingSwapNetUSD(ctx, posID)
	if err != nil {
		t.Fatal(err)
	}
	if got < 1.9599 || got > 1.9601 {
		t.Fatalf("应=income 1.46+swap 0.5, got %v", got)
	}
}

// TestD107_ZeroIncomeSumRealZero income 行存在但和=0（零 funding 期）→ 替换为 0（真零），
// 不回退估计——与 D-438 incomeRows>0 语义一致。
func TestD107_ZeroIncomeSumRealZero(t *testing.T) {
	s := d438Store(t)
	ctx := context.Background()
	posID := d438InsertPos(t, s)
	d438CF(t, s, posID, "funding", 3.18, "estimated")
	d438Income(t, s, posID, 0)

	got, err := s.fundingSwapNetUSD(ctx, posID)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("income 行>0 应替换为实结 0, got %v", got)
	}
}
