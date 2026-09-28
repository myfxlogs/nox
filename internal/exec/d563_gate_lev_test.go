package exec

// D-563 S1 T1：ensureGateLeverage——开仓前显式 SET 合约杠杆=推送条款。
// httptest 断言 POST 路径/参数（leverage+dual_side）；set-mark 命中不二次调；
// SET 失败/回显不符/目标未配置 → fail-closed 拒开不落单。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func d563GateLeverageSrv(t *testing.T, dualMode bool, echoLeverage string, failSet bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/futures/usdt/accounts":
			fmt.Fprintf(w, `{"in_dual_mode":%v}`, dualMode)
		case strings.HasSuffix(r.URL.Path, "/leverage"):
			posts.Add(1)
			if failSet {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprintf(w, `{"leverage":%q}`, echoLeverage)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &posts
}

// 双仓模式：POST /dual_comp/positions/{contract}/leverage?leverage=10&dual_side=dual_long；
// set-mark 命中 → 二次调用零新 POST。
func TestD563_EnsureGateLeverage_DualPost(t *testing.T) {
	srv, posts := d563GateLeverageSrv(t, true, "10", false)
	defer srv.Close()
	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	e.SetGateDefaultLeverage(10)
	ctx := context.Background()
	if err := e.ensureGateLeverage(ctx, "BTC_USDT", false); err != nil {
		t.Fatalf("SET 应成功: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("POST 应 1 次, got %d", posts.Load())
	}
	if err := e.ensureGateLeverage(ctx, "BTC_USDT", false); err != nil {
		t.Fatalf("set-mark 命中应直通: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("同键应防重复 SET, got %d", posts.Load())
	}
	// 卖腿 → dual_short 键独立。
	if err := e.ensureGateLeverage(ctx, "BTC_USDT", true); err != nil {
		t.Fatalf("short 侧 SET 应成功: %v", err)
	}
	if posts.Load() != 2 {
		t.Fatalf("另一侧应独立 SET, got %d", posts.Load())
	}
}

// 单仓模式：POST /positions/{contract}/leverage?leverage=10（无 dual_side）。
func TestD563_EnsureGateLeverage_SinglePost(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/futures/usdt/accounts" {
			io.WriteString(w, `{"in_dual_mode":false}`)
			return
		}
		if r.URL.Path == "/futures/usdt/positions/BTC_USDT/leverage" {
			gotQuery = r.URL.RawQuery
			io.WriteString(w, `{"leverage":"10"}`)
			return
		}
		t.Errorf("unexpected path %s", r.URL.Path)
	}))
	defer srv.Close()
	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	e.SetGateDefaultLeverage(10)
	if err := e.ensureGateLeverage(context.Background(), "BTC_USDT", false); err != nil {
		t.Fatalf("SET 应成功: %v", err)
	}
	if gotQuery != "leverage=10" {
		t.Fatalf("query=%q want leverage=10（无 dual_side）", gotQuery)
	}
}

// SET 失败 / 回显不符 / 目标未配置 → fail-closed 错误。
func TestD563_EnsureGateLeverage_FailClosed(t *testing.T) {
	srv, posts := d563GateLeverageSrv(t, true, "10", true) // SET 恒 500
	defer srv.Close()
	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	e.SetGateDefaultLeverage(10)
	if err := e.ensureGateLeverage(context.Background(), "BTC_USDT", false); err == nil {
		t.Fatal("SET 500 应 fail-closed error")
	}
	if posts.Load() == 0 {
		t.Fatal("SET 应实调")
	}
	// 回显不符。
	srv2, _ := d563GateLeverageSrv(t, true, "8", false)
	defer srv2.Close()
	e2 := NewCEXExecutor("gate", "perp", "k", "s", "", srv2.URL, true)
	e2.SetGateDefaultLeverage(10)
	if err := e2.ensureGateLeverage(context.Background(), "BTC_USDT", false); err == nil ||
		!strings.Contains(err.Error(), "回显") {
		t.Fatalf("回显≠目标应 fail-closed, got %v", err)
	}
	// 目标未配置 → 零 HTTP。
	srv3, posts3 := d563GateLeverageSrv(t, true, "10", false)
	defer srv3.Close()
	e3 := NewCEXExecutor("gate", "perp", "k", "s", "", srv3.URL, true)
	if err := e3.ensureGateLeverage(context.Background(), "BTC_USDT", false); err == nil ||
		!strings.Contains(err.Error(), "未配置") {
		t.Fatalf("未配置应 fail-closed, got %v", err)
	}
	if posts3.Load() != 0 {
		t.Fatal("未配置应零 POST")
	}
}
