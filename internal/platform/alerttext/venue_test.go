package alerttext

import (
	"strings"
	"testing"
	"time"
)

// 表驱动：生产实拍串 + 两前缀形态 + 未识别/中文/非 venue/畸形全透出。
func TestVenueErrCN(t *testing.T) {
	banLocal := time.UnixMilli(1790736439136).Format("2006-01-02 15:04:05")
	cases := []struct {
		name  string
		in    string
		wants []string // 命中时输出应含子串
		same  bool     // 逐字节不变透出
	}{
		{
			name:  "生产实拍 status 418 -1003 ban",
			in:    `fetch binance PAXGUSDT: premiumIndex: status 418: {"code":-1003,"msg":"Way too many requests; IP(192.229.85.35) banned until 1790736439136. Please use the websocket for live updates to avoid bans."}`,
			wants: []string{"fetch binance PAXGUSDT: premiumIndex:", "请求频率超限，交易所已临时封禁出口IP至 " + banLocal, "（错误码 -1003）"},
		},
		{
			name:  "exec 形态 http 418 同体",
			in:    `okx: http 418: {"code":-1003,"msg":"Way too many requests; IP(1.2.3.4) banned until 1790736439136."}`,
			wants: []string{"okx:", "封禁出口IP至 " + banLocal},
		},
		{
			name:  "status 429 -1003 无 ban 时戳",
			in:    `status 429: {"code":-1003,"msg":"Too many requests"}`,
			wants: []string{"请求频率超限（HTTP 429，错误码 -1003）——降低REST频率或改WS"},
		},
		{name: "中文告警直通", in: "MT5 数据监控：报价断流 5 分钟", same: true},
		{name: "非 venue 英文透出", in: "dial tcp: connection refused", same: true},
		{name: "壳内非 JSON 透出", in: `status 418: {not-json`, same: true},
		{name: "未识别 code 透出", in: `status 400: {"code":-1100,"msg":"illegal chars"}`, same: true},
	}
	for _, c := range cases {
		got := VenueErrCN(c.in)
		if c.same {
			if got != c.in {
				t.Fatalf("%s: 应原样透出 got %q", c.name, got)
			}
			continue
		}
		for _, w := range c.wants {
			if !strings.Contains(got, w) {
				t.Fatalf("%s: 缺 %q got %q", c.name, w, got)
			}
		}
		if strings.Contains(got, "banned until") || strings.Contains(got, "Too many requests") {
			t.Fatalf("%s: 英文壳未替换 %q", c.name, got)
		}
	}
}
