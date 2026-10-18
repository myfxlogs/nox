package dciexec

import (
	"context"
	"strings"
	"testing"
)

// fakeModeGate D-620 T4：控制面测试注口（CheckGate 接口形）。
type fakeModeGate struct {
	mode    string
	allowed bool
	reason  string
}

func (f fakeModeGate) CheckGate(context.Context) (string, bool, string, error) {
	return f.mode, f.allowed, f.reason, nil
}

// liveModeGate D-620 T4 基件：各测试文件默认 LIVE 授权态判准（nil 时 GoProposal 拒「控制面未接线」）。
var liveModeGate = fakeModeGate{mode: "LIVE", allowed: true}

// TestGoProposal_ModeGate D-620 T4：GoProposal 首步三拒——
// nil=「控制面未接线」、!allowed=拒带因、mode≠LIVE=拒；LIVE 放行入原径。
func TestGoProposal_ModeGate(t *testing.T) {
	ctx := context.Background()
	// nil → 控制面未接线
	ex := &Executor{Store: newFake()}
	if _, err := ex.GoProposal(ctx, 1); err == nil || !strings.Contains(err.Error(), "控制面未接线") {
		t.Fatalf("nil ModeGate: err=%v", err)
	}
	// allowed=false → 拒带因
	ex = &Executor{Store: newFake(), ModeGate: fakeModeGate{mode: "PAPER", allowed: false, reason: "kill switch active"}}
	if _, err := ex.GoProposal(ctx, 1); err == nil || !strings.Contains(err.Error(), "控制面拒执行") {
		t.Fatalf("not allowed: err=%v", err)
	}
	// PAPER allowed → 非 LIVE 拒
	ex = &Executor{Store: newFake(), ModeGate: fakeModeGate{mode: "PAPER", allowed: true}}
	if _, err := ex.GoProposal(ctx, 1); err == nil || !strings.Contains(err.Error(), "非 LIVE") {
		t.Fatalf("PAPER: err=%v", err)
	}
	// LIVE → 进原径（fake store 无此提案→「查询败」而非闸拒）
	ex = &Executor{Store: newFake(), ModeGate: fakeModeGate{mode: "LIVE", allowed: true}}
	_, err := ex.GoProposal(ctx, 999)
	if err == nil {
		t.Fatal("LIVE 径应达提案查询（无行→查询败）")
	}
	if strings.Contains(err.Error(), "控制面") || strings.Contains(err.Error(), "非 LIVE") {
		t.Fatalf("LIVE 被闸误拦: %v", err)
	}
}
