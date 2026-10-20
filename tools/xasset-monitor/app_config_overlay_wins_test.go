package main

// 锚：T1 env 置空+app_config 行→端点走 DB 源；T2 双缺→error 含 app_config
// 且 reportFatal 落 critical alerts 行；T3 30min 同类去重（连发只落行不重推）。
import (
	"context"
	"errors"
	"nox/internal/platform/alerttext"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func d558Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s, err := pgstore.New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
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

// T1（D-145 改锚）：app_config 行 → binanceProxyURL 回 DB 值（唯一权威，env 读径封死）。
func TestD558_AppConfigOverlayWins(t *testing.T) {
	s := d558Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO app_config (key, value) VALUES ('BINANCE_PROXY_URL', 'https://db-proxy.example')`); err != nil {
		t.Fatalf("seed app_config: %v", err)
	}
	got, err := binanceProxyURL(ctx, s)
	if err != nil || got != "https://db-proxy.example" {
		t.Fatalf("want DB 值, got %q err=%v", got, err)
	}
}

// T2：app_config 无行 → error 含 app_config；reportFatal 落 critical 行。
func TestD558_MissingSourceFatal(t *testing.T) {
	s := d558Store(t)
	ctx := context.Background()
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

	// InsertAlertDedup 24h 窗（告警风暴 dedup 票）——首发保留落行，
	// 窗内重发插行被抑制（1 行在档）；30min 同键判定仅影响飞书推送面。
	mapped := "xasset-monitor " + fatal.Error() + alerttext.SeatDispatchLine("实施", "xasset 采集排障（读 STATE 本行）")
	reportFatal(ctx, s, fatal) // 首发生：落行+推（凭据缺席只 log）
	if !hasRecentSameKeyAlert(ctx, s, "critical", mapped) {
		t.Fatal("首发生后 30min 内应有同键 critical 在档")
	}
	reportFatal(ctx, s, fatal) // 24h 窗内重发：InsertAlertDedup 抑制插行（首发保留）
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical' AND message LIKE 'xasset-monitor%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("24h 窗内同键重发应被 dedup 抑制（首发保留=仍 1 行）n=%d err=%v", n, err)
	}
	// 超窗：把在档行 ts 拨回 25h 前 → dedup 窗与同键窗皆过期，重发落第二行。
	if _, err := s.Pool().Exec(ctx,
		`UPDATE alerts SET ts = now()-interval '25 hours' WHERE level='critical'`); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if hasRecentSameKeyAlert(ctx, s, "critical", mapped) {
		t.Fatal("25h 前行应判为已超 30min 同键窗（非 dup）")
	}
	reportFatal(ctx, s, fatal)
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical' AND message LIKE 'xasset-monitor%'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("超 24h 窗重发应落第二行（新事件再告警）n=%d err=%v", n, err)
	}
}

// nil store / nil err 优雅退化（pg 未连上的形态：只 log 不 panic）。
func TestD558_ReportFatalNilSafe(t *testing.T) {
	reportFatal(context.Background(), nil, errors.New("pg down"))
	reportFatal(context.Background(), nil, nil)
}

// venue 英文错误 fatal → reportFatal → alerts 行 message 已中文化
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
