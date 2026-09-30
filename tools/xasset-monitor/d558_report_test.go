package main

// D-558 锚：T1 env 置空+app_config 行→端点走 DB 源；T2 双缺→error 含 app_config
// 且 reportFatal 落 critical alerts 行；T3 30min 同类去重（连发只落行不重推）。
import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

func d558Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s, err := pgstore.New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts, app_config`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// T1：env 置空 + app_config 行 → binanceProxyURL 回 DB 值且 os.Getenv 已被 overlay 置位。
func TestD558_AppConfigOverlayWins(t *testing.T) {
	s := d558Store(t)
	ctx := context.Background()
	t.Setenv("BINANCE_PROXY_URL", "")
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO app_config (key, value) VALUES ('BINANCE_PROXY_URL', 'https://db-proxy.example')`); err != nil {
		t.Fatalf("seed app_config: %v", err)
	}
	got, err := binanceProxyURL(ctx, s)
	if err != nil || got != "https://db-proxy.example" {
		t.Fatalf("want DB 值, got %q err=%v", got, err)
	}
	if v := os.Getenv("BINANCE_PROXY_URL"); v != "https://db-proxy.example" {
		t.Fatalf("overlay 后 env 应置位 DB 值, got %q", v)
	}
	// DB>env 次序：env 有不同值时 DB 行覆盖。
	t.Setenv("BINANCE_PROXY_URL", "https://env-proxy.example")
	got, err = binanceProxyURL(ctx, s)
	if err != nil || got != "https://db-proxy.example" {
		t.Fatalf("DB 行应覆盖 env, got %q err=%v", got, err)
	}
}

// T2：app_config 无行 + env 未设 → error 含 app_config；reportFatal 落 critical 行。
func TestD558_MissingSourceFatal(t *testing.T) {
	s := d558Store(t)
	ctx := context.Background()
	t.Setenv("BINANCE_PROXY_URL", "")
	got, err := binanceProxyURL(ctx, s)
	if err == nil || got != "" {
		t.Fatalf("双缺应报错, got %q err=%v", got, err)
	}
	if !strings.Contains(err.Error(), "app_config") {
		t.Fatalf("error 应含 app_config 字样: %v", err)
	}
	reportFatal(ctx, s, err)
	var n int
	if qerr := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical' AND message LIKE 'xasset-monitor%'`).Scan(&n); qerr != nil || n != 1 {
		t.Fatalf("critical 行应落库 n=%d err=%v", n, qerr)
	}
}

// T3：30min 内同类 fatal → 落行不重复推飞书；超窗 → 再推。
// 飞书发送由 app_secrets 凭据门把守——测试库无凭据行=发送自然跳过，此处钉
// ①落行语义 ②去重查询 ③凭据缺席零 panic 不掩盖原 err。
func TestD558_FatalDedup30min(t *testing.T) {
	s := d558Store(t)
	ctx := context.Background()
	fatal := errors.New("BINANCE_PROXY_URL 未配置（app_config 无行且 env 未设）")

	reportFatal(ctx, s, fatal) // 首发生：落行+推（凭据缺席只 log）
	if hasRecentCriticalAlert(ctx, s) != true {
		t.Fatal("首发生后 30min 内应有同类 critical 在档")
	}
	reportFatal(ctx, s, fatal) // 30min 内重发：仍落行、飞书去重
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical' AND message LIKE 'xasset-monitor%'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("两次 fatal 应各落一行 n=%d err=%v", n, err)
	}
	// 超窗：把在档行 ts 拨回 31min 前 → 去重判否。
	if _, err := s.Pool().Exec(ctx,
		`UPDATE alerts SET ts = now()-interval '31 minutes' WHERE level='critical'`); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if hasRecentCriticalAlert(ctx, s) {
		t.Fatal("31min 前行应判为已超窗（非 dup）")
	}
}

// nil store / nil err 优雅退化（pg 未连上的形态：只 log 不 panic）。
func TestD558_ReportFatalNilSafe(t *testing.T) {
	reportFatal(context.Background(), nil, errors.New("pg down"))
	reportFatal(context.Background(), nil, nil)
}

// D-588：venue 英文错误 fatal → reportFatal → alerts 行 message 已中文化
// （insertAlert 漏斗单点挂点）且 xasset-monitor 前缀保留（去重 LIKE 不破）。
func TestD558_VenueErrAlertCN(t *testing.T) {
	s := d558Store(t)
	ctx := context.Background()
	fatal := errors.New(`fetch binance PAXGUSDT: premiumIndex: status 418: {"code":-1003,"msg":"Way too many requests; IP(192.229.85.35) banned until 1790736439136. Please use the websocket for live updates to avoid bans."}`)

	reportFatal(ctx, s, fatal)
	var msg string
	if err := s.Pool().QueryRow(ctx,
		`SELECT message FROM alerts WHERE level='critical'`).Scan(&msg); err != nil {
		t.Fatalf("critical 行缺失: %v", err)
	}
	if !strings.HasPrefix(msg, "xasset-monitor ") {
		t.Fatalf("前缀保留（去重 LIKE 依赖）: %q", msg)
	}
	for _, w := range []string{"请求频率超限，交易所已临时封禁出口IP至", "（错误码 -1003）", "fetch binance PAXGUSDT: premiumIndex:"} {
		if !strings.Contains(msg, w) {
			t.Fatalf("缺 %q: %q", w, msg)
		}
	}
	if strings.Contains(msg, "banned until") {
		t.Fatalf("英文壳未替换: %q", msg)
	}
}
