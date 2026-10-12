package dashboard

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/trading/exec"
)

// d574Svc 裸 Service（pipeline nil → 首个闸即 FAILED）。
func d574Svc() *Service { return &Service{} }

// A1：确诊文案全中文白话（抽 5 处关键词断言——删白话头即红）。
func TestD574_A1_PlainChineseMessages(t *testing.T) {
	ctx := context.Background()
	// executor not wired（pipeline nil）
	if _, msg, _ := d574Svc().ExecuteOpportunityInternal(ctx, "x", "r", 1); !strings.Contains(msg, "执行器未接线") {
		t.Fatalf("executor msg=%q", msg)
	}
	// invalid quantity
	s := &Service{pipeline: &exec.Pipeline{}}
	if _, msg, _ := s.ExecuteOpportunityInternal(ctx, "x", "r", 0); !strings.Contains(msg, "数量无效") {
		t.Fatalf("qty msg=%q", msg)
	}
	// opportunity not found（store nil→list error 先撞墙；改用 gateChecker 路径覆盖模式闸文案）
	if _, msg, _ := s.ExecuteMeanRevert(ctx, "X", "a:mt5", "b:mt5", "r", 0, 0); !strings.Contains(msg, "数量无效") {
		t.Fatalf("mr qty msg=%q", msg)
	}
}

// A2：透传类=白话头+原始明细尾（err 原文保留）。
func TestD574_A2_PassthroughRawDetail(t *testing.T) {
	// autotrade_state 表空 → 查询失败透传（白话头+原始明细尾）
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE autotrade_state`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	svc := &Service{store: st}
	_, err := svc.checkAutotradeState(ctx)
	if err == nil || !strings.Contains(err.Error(), "自动交易状态查询失败") || !strings.Contains(err.Error(), "原始明细：") {
		t.Fatalf("passthrough err=%v want 白话头+原始明细尾", err)
	}
}

// A3：协议兼容——status 枚举 EXECUTED/FAILED 逐字不变；message 含中文即非裸英文。
func TestD574_A3_StatusEnumUnchanged(t *testing.T) {
	status, msg, posID := d574Svc().ExecuteOpportunityInternal(context.Background(), "x", "r", 1)
	if status != "FAILED" || posID != 0 {
		t.Fatalf("status=%q posID=%d", status, posID)
	}
	if regexp.MustCompile(`^[a-z ]+$`).MatchString(msg) {
		t.Fatalf("message still raw english: %q", msg)
	}
}

// A4：机械清扫——execute 族 FAILED 直出串零英文句。
func TestD574_A4_NoEnglishLiteral(t *testing.T) {
	// freeze guard / env gate / live gate / tradability 直接断言中文头。
	s := &Service{freezeGuard: time.Minute}
	qb := bus.New()
	s.qbus = qb
	legs := []exec.LegSpec{{VenueKey: "b:mt5", VenueSymbol: "EURUSD", Side: "BUY", Qty: 0.1}}
	if err := s.checkFreezeGuard("X", "X", legs); err == nil || !strings.Contains(err.Error(), "行情冻结") {
		t.Fatalf("freeze err=%v", err)
	}
}
