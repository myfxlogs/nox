// go_audit_test.go —— 口令执行审计行锚（10-22 缺口：feishu 不可达期执行
// 回执零落账）：go 成功/拒单/decline 三终态各落一行（info=成/decline、
// warn=拒带因签名），feishu 透出径不动（审计行是档非达）。
package dciexec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func auditAlerts(fs *fakeStore, want string) bool {
	for _, a := range fs.alerts {
		if strings.Contains(a, "提案执行审计") && strings.Contains(a, want) {
			return true
		}
	}
	return false
}

// go 成功径→info 审计行（提案 id+模板+executed+耗时）。
func TestGoAudit_Executed(t *testing.T) {
	fs := newFake()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"USDT": 1e6, "BTC": 0.5})
	pid := fs.addProposal("dci_buy_low")
	if _, err := e.GoProposal(context.Background(), pid); err != nil {
		t.Fatalf("go 败: %v", err)
	}
	if !auditAlerts(fs, "executed") {
		t.Fatalf("executed 审计行缺: %+v", fs.alerts)
	}
	if !auditAlerts(fs, fmt.Sprintf("#%d dci_buy_low", pid)) {
		t.Fatalf("审计行缺提案 id+模板: %+v", fs.alerts)
	}
}

// go 拒单径→warn 审计行带拒因签名（控制面未接线=闸名+因）。
func TestGoAudit_Reject(t *testing.T) {
	fs := newFake()
	e := &Executor{Store: fs, Now: time.Now} // ModeGate=nil→首闸拒
	pid := fs.addProposal("dci_buy_low")
	if _, err := e.GoProposal(context.Background(), pid); err == nil {
		t.Fatal("控制面未接线应拒")
	}
	var sawWarn bool
	for _, a := range fs.alerts {
		if strings.HasPrefix(a, "warn: 提案执行审计") && strings.Contains(a, "reject") &&
			strings.Contains(a, "控制面未接线") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("reject warn 审计行缺/级错: %+v", fs.alerts)
	}
}

// decline 终态→info 审计行 outcome=declined。
func TestGoAudit_Decline(t *testing.T) {
	fs := newFake()
	e := &Executor{Store: fs, Now: time.Now}
	pid := fs.addProposal("dci_buy_low")
	if _, err := e.DeclineProposal(context.Background(), pid); err != nil {
		t.Fatalf("decline 败: %v", err)
	}
	if !auditAlerts(fs, "declined") {
		t.Fatalf("declined 审计行缺: %+v", fs.alerts)
	}
}

// 裸 go 无提案→warn 审计行（前置段自落——GoProposal 层不双记）。
func TestGoAudit_BareGoNoProposal(t *testing.T) {
	fs := newFake()
	e := &Executor{Store: fs, Now: time.Now}
	if _, err := e.Go(context.Background()); err == nil {
		t.Fatal("空提案应拒")
	}
	var n int
	for _, a := range fs.alerts {
		if strings.Contains(a, "提案执行审计") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("裸 go 无提案应恰好一条审计行: %+v", fs.alerts)
	}
}
