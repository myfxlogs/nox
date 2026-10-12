// okx_private_ws_test.go —— D-383 B2/B4：OKX private account WS 单元锚。
// 锚：login 帧形状+签名确定性（B2）、凭据零暴露（B4）、推送→快照行映射与 REST accountOKX
// 同口径（B2，USDT-only D-364）、断流守卫冷却（B3）。
package exchange

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// TestD383B2_LoginFrameAndSign B2：签名=base64(HMAC-SHA256(secret, ts+"GET"+"/users/self/verify"))
// （期望值离线预计算）；帧只含 apiKey/passphrase/timestamp/sign 四键——secret 不进帧（B4）。
func TestD383B2_LoginFrameAndSign(t *testing.T) {
	const ts = "2026-09-22T00:00:00.000Z"
	const want = "3johKPZs7sfE6CPMMnu8a8ISVSlHsubzTS5OovZf+qQ=" // secret=secret 离线 HMAC 预计算
	got := okxPrivateSign("secret", ts+"GET"+"/users/self/verify")
	if got != want {
		t.Fatalf("sign = %s, want %s（红=签名算法漂移，login 必被 OKX 拒）", got, want)
	}
	frame := okxPrivateLoginFrame("my-key", "my-pass", ts, got)
	if frame["op"] != "login" {
		t.Fatalf("op = %v, want login", frame["op"])
	}
	args, ok := frame["args"].([]map[string]string)
	if !ok || len(args) != 1 {
		t.Fatalf("args = %#v, want 单元素列表", frame["args"])
	}
	a := args[0]
	wantKeys := map[string]string{
		"apiKey": "my-key", "passphrase": "my-pass", "timestamp": ts, "sign": want,
	}
	if !reflect.DeepEqual(a, wantKeys) {
		t.Fatalf("login args = %#v, want %#v", a, wantKeys)
	}
	// B4：帧序列化不含 secret（secret 只参与签名，不上帧）。
	b, _ := json.Marshal(frame)
	if strings.Contains(string(b), "my-secret") {
		t.Errorf("login 帧含 secret 痕迹: %s", b)
	}
}

// TestD383B2_PushRowMapping B2：推送 → 快照行映射与 REST accountOKX 同口径——
// equity=Σ details[USDT].eqUsd（USDT-only，D-364 业主令非 USDT 标不读取），balance=equity，
// currency=USDT，src=okx_event；totalEq 全币值不采用（对拍口径一致性）。
func TestD383B2_PushRowMapping(t *testing.T) {
	now := time.Now().UTC()
	data := map[string]any{
		"totalEq": "1234.5", // 全币总权益（含 DOGE 折算）——REST 口径不采用
		"details": []map[string]any{
			{"ccy": "USDT", "eq": "100.5", "eqUsd": "100.5"},
			{"ccy": "DOGE", "eq": "900", "eqUsd": "1134.0"},
		},
	}
	row, _, ok := okxAccountPushRow(data, now)
	if !ok {
		t.Fatal("合法推送应解析成功")
	}
	if row.BalanceRaw != 100.5 || row.EquityRaw != 100.5 {
		t.Fatalf("balance/equity = %v/%v, want 100.5/100.5（USDT-only 对拍口径；红=误用 totalEq 全币值）",
			row.BalanceRaw, row.EquityRaw)
	}
	if row.Venue != "okx" || row.VenueType != "unified" || row.Currency != "USDT" || row.Src != "okx_event" {
		t.Fatalf("行标记 = %s/%s/%s/%s, want okx/unified/USDT/okx_event", row.Venue, row.VenueType, row.Currency, row.Src)
	}
	if row.FreeMarginRaw != 0 {
		t.Errorf("free_margin = %v, want 0（REST accountOKX 同缺省不采）", row.FreeMarginRaw)
	}
	if !row.FetchedAt.Equal(now) {
		t.Errorf("fetched_at = %v, want 推送时刻 %v", row.FetchedAt, now)
	}

	// details 无 USDT 行 → eq=0 行照落（REST 同不 panic 语义）
	row, _, ok = okxAccountPushRow(map[string]any{"details": []map[string]any{{"ccy": "DOGE", "eqUsd": "5"}}}, now)
	if !ok || row.EquityRaw != 0 {
		t.Fatalf("无 USDT 行: ok=%v eq=%v, want true/0", ok, row.EquityRaw)
	}
	// eqUsd 解析失败跳该币不整行弃
	row, _, ok = okxAccountPushRow(map[string]any{"details": []map[string]any{
		{"ccy": "USDT", "eqUsd": "oops"},
		{"ccy": "USDT", "eqUsd": "7.25"},
	}}, now)
	if !ok || row.EquityRaw != 7.25 {
		t.Fatalf("坏值跳过: ok=%v eq=%v, want true/7.25", ok, row.EquityRaw)
	}
}

// TestD383B3_StaleGuardCooldown B3：静默超阈值 warn 一次→冷却期内不再报→冷却后可再报；
// 订阅成功前（未 touch）不报。
func TestD383B3_StaleGuardCooldown(t *testing.T) {
	g := newStaleGuard()
	t0 := time.Now().UTC()
	const idle, cooldown = 30 * time.Minute, 30 * time.Minute
	if g.warnDue(t0.Add(45*time.Minute), idle, cooldown) {
		t.Fatal("订阅成功前不应告警（未 touch 无基准）")
	}
	g.touch(t0) // 订阅成功=基准点
	if g.warnDue(t0.Add(29*time.Minute), idle, cooldown) {
		t.Fatal("29min 静默未达 30min 阈值不应告警")
	}
	if !g.warnDue(t0.Add(31*time.Minute), idle, cooldown) {
		t.Fatal("31min 静默应告警一次")
	}
	if g.warnDue(t0.Add(45*time.Minute), idle, cooldown) {
		t.Fatal("冷却期内不应重复告警（单次冷却，红=冷却缺失）")
	}
	if !g.warnDue(t0.Add(61*time.Minute), idle, cooldown) {
		t.Fatal("冷却期满后静默持续应再告警")
	}
	g.touch(t0.Add(62 * time.Minute)) // 恢复推送 → 计时重置
	if g.warnDue(t0.Add(80*time.Minute), idle, cooldown) {
		t.Fatal("恢复推送后 18min 不应告警（计时重置）")
	}
}

// TestD383B4_SnapshotSinkSignature B4 编译面：*pgstore.Store 满足 VenueSnapshotSink
// （装配点 store 直传的接口契约在案）。
func TestD383B4_SnapshotSinkSignature(t *testing.T) {
	var _ VenueSnapshotSink = (*pgstore.Store)(nil)
}

// TestD383B2R1_LoginFrameSignPinned R1 返修锚：钉调用点组合（不只测纯函数）——
// login 帧 sign 必须=base64(HMAC-SHA256(secret, ts+"GET"+"/users/self/verify"))。
// 固定 ts/key/secret 期望值离线预计算；prehash 缺 "GET"+path 时本锚必红（OKX 拒签）。
func TestD383B2R1_LoginFrameSignPinned(t *testing.T) {
	const ts = "2026-09-22T00:00:00.000Z"
	const want = "3johKPZs7sfE6CPMMnu8a8ISVSlHsubzTS5OovZf+qQ=" // secret=secret 离线预计算
	o := NewOKXPrivateWS("my-key", "secret", "my-pass", false, nil, "okx:perp", nil, NewOrderEventCache(), NewPositionEventCache(), OKXPrivateHooks{})
	frame := o.loginFrame(ts)
	args, ok := frame["args"].([]map[string]string)
	if !ok || len(args) != 1 {
		t.Fatalf("login args 形状错: %#v", frame["args"])
	}
	if got := args[0]["sign"]; got != want {
		t.Fatalf("login sign = %s, want %s（红=R1 prehash 组合错，OKX 必拒签死循环）", got, want)
	}
}

// TestD383B2R3_LoginTsUnixSeconds R3 返修锚：login ts 必须=Unix epoch 秒串（SSOT §5 B.2
// 订正）——全数字可 ParseInt；ISO8601 残留（REST 头格式误植）时本锚必红（OKX 60004）。
func TestD383B2R3_LoginTsUnixSeconds(t *testing.T) {
	if got := okxPrivateLoginTs(time.Unix(1538054050, 0)); got != "1538054050" {
		t.Fatalf("login ts = %q, want Unix 秒串 1538054050（红=ISO8601 残留）", got)
	}
	n, err := strconv.ParseInt(okxPrivateLoginTs(time.Now()), 10, 64)
	if err != nil || n <= 0 {
		t.Fatalf("login ts 须全数字可 ParseInt: %q err=%v（SSOT 锚字面）", okxPrivateLoginTs(time.Now()), err)
	}
}
