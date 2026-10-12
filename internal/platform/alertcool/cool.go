// Package alertcool 告警同指纹冷却（D-301c S1）。
// 噪音源范式：连败告警消息含递增计数+错误明细（如 deribit 72 条/时），指纹各异绕过下游节流。
// Cooler 按「level+归一化消息」指纹在窗口内只放首条（携带当时的计数/明细），窗口过期再放一条；
// 状态转换类（crit↔recovered 指纹不同）不受影响；窗口内 flap 的同态重复被吞=同一进行中故障的
// 已声明取舍（D-301c §2）。收口面=cmd/core 四处 AlertFn 注入点（Wrap1/Wrap2/Wrap3），
// 底层 InsertAlert 直调（~80 处）不在本包范围。Window=30min 对齐 critRealertInterval，v1 零配置面。
package alertcool

import (
	"regexp"
	"sync"
	"time"
)

// DefaultWindow 默认冷却窗（对齐 dashboard.critRealertInterval 30min）。
const DefaultWindow = 30 * time.Minute

// 占位归一化规则（按序应用，D-301c §2 钉死）：hex 串→#h、IPv4[:port]→#ip、时长→#dur、裸数字→#n。
// 计数 3→4→72 归一为 #n 同指纹；不同 venue/错误类别字面仍异指纹。
var (
	hexRe = regexp.MustCompile(`[0-9a-fA-F]{8,}`)
	ipRe  = regexp.MustCompile(`\d+\.\d+\.\d+\.\d+(:\d+)?`)
	durRe = regexp.MustCompile(`\d+(\.\d+)?(ns|ms|s|m|h|d)\b`)
	numRe = regexp.MustCompile(`\d+(\.\d+)?`)
)

// Normalize 消息归一化：数字/时长/hex/IP → 占位符（规则序 hex→ip→dur→num，占位符无数字不回配）。
func Normalize(msg string) string {
	out := hexRe.ReplaceAllString(msg, "#h")
	out = ipRe.ReplaceAllString(out, "#ip")
	out = durRe.ReplaceAllString(out, "#dur")
	out = numRe.ReplaceAllString(out, "#n")
	return out
}

// Cooler 同指纹告警冷却器（并发安全；now 可注入测试）。
type Cooler struct {
	Window time.Duration
	mu     sync.Mutex
	last   map[string]time.Time // fingerprint → 上次放行时刻
	now    func() time.Time
}

// New 创建冷却器（window<=0 → DefaultWindow）。
func New(window time.Duration) *Cooler {
	if window <= 0 {
		window = DefaultWindow
	}
	return &Cooler{Window: window, last: make(map[string]time.Time), now: time.Now}
}

// Allow 首次/窗口外 → true 并记放行时刻；窗口内同指纹 → false。
// 指纹 = level + "|" + Normalize(msg)。
func (c *Cooler) Allow(level, msg string) bool {
	fp := level + "|" + Normalize(msg)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if last, ok := c.last[fp]; ok && now.Sub(last) < c.Window {
		return false
	}
	c.last[fp] = now
	return true
}

// Wrap1 包装 func(level, msg string) 注入点：被抑制的调用不落底层。
func (c *Cooler) Wrap1(fn func(level, msg string)) func(level, msg string) {
	return func(level, msg string) {
		if c.Allow(level, msg) {
			fn(level, msg)
		}
	}
}

// Wrap2 包装 func(level, msg string) error 注入点：被抑制 → 返回 nil（未发生=无错误）。
func (c *Cooler) Wrap2(fn func(level, msg string) error) func(level, msg string) error {
	return func(level, msg string) error {
		if !c.Allow(level, msg) {
			return nil
		}
		return fn(level, msg)
	}
}

// Wrap3 包装 func(posID *int64, level, msg string) 注入点：posID 不入指纹——同仓同因重复告警
// 同样该冷，posID 差异属实例明细非类别（D-301c §2 论证）。
func (c *Cooler) Wrap3(fn func(posID *int64, level, msg string)) func(posID *int64, level, msg string) {
	return func(posID *int64, level, msg string) {
		if c.Allow(level, msg) {
			fn(posID, level, msg)
		}
	}
}
