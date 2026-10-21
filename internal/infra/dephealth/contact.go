// contact.go —— 投资人接触面犬哨（STATE 行「投资人接触面犬哨」/D-651 审计⑥）。
// 病灶：入站命令径（feishu webhook）历史命中=0 死约一月系统零自报——交易面有
// 自盯、投资人面零犬哨。三通道：feishu_webhook 入站事件 / rpc_stream 前端
// StreamUpdates / command 命令处理径（ack/go 链路）。记 last_hit+累计计数
// （拒因计数器同族形态——只测不判零业务副作用）；「应活」（Register 装配）且
// 零命中超阈→ContactSuspects 返嫌疑集，调用面 warn 标报；有命中自然清态。
package dephealth

import (
	"sync"
	"time"
)

// ContactRow 接触面通道快照行（last_hit+命中计数）。
type ContactRow struct {
	Channel          string
	Hits             uint64
	LastHitUnix      int64
	RegisteredAtUnix int64 // 注册时标（返修②——Suspects 须先满观察阈：注册即嫌=假阳）
}

var contactReg = struct {
	sync.Mutex
	rows map[string]*ContactRow
}{rows: map[string]*ContactRow{}}

// ContactRegister 装配时显式注册「应活」通道（未注册面不标报——同 rejects
// 预注册语义：键集静态非运行时自造）。
func ContactRegister(channels ...string) {
	contactReg.Lock()
	defer contactReg.Unlock()
	for _, ch := range channels {
		if contactReg.rows[ch] == nil {
			contactReg.rows[ch] = &ContactRow{Channel: ch, RegisteredAtUnix: time.Now().Unix()}
		}
	}
}

// ContactHit 通道命中记账（命中即清嫌疑——last_hit 刷新）。
func ContactHit(channel string) {
	contactReg.Lock()
	defer contactReg.Unlock()
	r := contactReg.rows[channel]
	if r == nil { // 未注册命中=同 rejects 哨兵语义——记零计数面不建态
		return
	}
	r.Hits++
	r.LastHitUnix = time.Now().Unix()
}

// ContactSnapshot dump 面（拷贝快照——dephealth ListSourceHealth 透出用）。
func ContactSnapshot() []ContactRow {
	contactReg.Lock()
	defer contactReg.Unlock()
	out := make([]ContactRow, 0, len(contactReg.rows))
	for _, r := range contactReg.rows {
		out = append(out, *r)
	}
	return out
}

// ContactSuspects 死径嫌疑集（返修②观察阈版）：须同时成立——①注册满
// idleFor（观察窗未满不判嫌——注册即嫌=假阳：boot 后 72h 窗内自然零命中
// 非死径）＋②零命中（从未命中）或末次命中龄>idleFor。
func ContactSuspects(idleFor time.Duration) []ContactRow {
	contactReg.Lock()
	defer contactReg.Unlock()
	now := time.Now()
	var out []ContactRow
	for _, r := range contactReg.rows {
		if now.Sub(time.Unix(r.RegisteredAtUnix, 0)) <= idleFor {
			continue // 观察窗未满
		}
		if r.Hits == 0 || now.Sub(time.Unix(r.LastHitUnix, 0)) > idleFor {
			out = append(out, *r)
		}
	}
	return out
}
