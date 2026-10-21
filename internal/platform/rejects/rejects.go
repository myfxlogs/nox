// Package rejects —— D-652 A 段：判定路径拒因可观测计数器。
// 病灶锚=静默杀三连（xasset tryPair 六门全 return nil 零痕等）——判定面死伤
// 不可见。Key 面=判定面名×拒因名（常量预注册——热路径原子加零分配，键爆炸
// 结构性防）；零业务副作用（只数不判）；dump 搭车 mt-health/digest 双既有周期。
package rejects

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Key 面=判定面名×门=拒因名（常量；热路径零分配——字段直比）。
type Key struct{ Face, Gate string }

// Row dump 行：N=累计，First/Last=首末拒时点。
type Row struct {
	Key
	N           uint64
	First, Last time.Time
}

type cell struct {
	n           atomic.Uint64
	first, last atomic.Int64 // unixnano
}

var reg struct {
	sync.Mutex
	cells map[Key]*cell
}

var warnedMiss sync.Map // 未注册键 boot warn 一次（接线漏挂早号）

// Register 装配时显式注册门集（键集静态——热路径零 map 增长）。
func Register(face string, gates ...string) {
	reg.Lock()
	defer reg.Unlock()
	if reg.cells == nil {
		reg.cells = map[Key]*cell{}
	}
	for _, g := range gates {
		reg.cells[Key{face, g}] = &cell{}
	}
}

// Inc 判定点原子+1（未注册键=忽略+boot warn 一次——接线漏挂早号不静默）。
func Inc(k Key) {
	reg.Lock()
	c := reg.cells[k]
	reg.Unlock()
	if c == nil {
		if _, dup := warnedMiss.LoadOrStore(k, true); !dup {
			log.Printf("[rejects] 未注册键忽略（接线漏挂？）: %s.%s", k.Face, k.Gate)
		}
		return
	}
	now := time.Now().UnixNano()
	c.n.Add(1)
	if c.first.Load() == 0 {
		c.first.CompareAndSwap(0, now)
	}
	c.last.Store(now)
}

// Snapshot dump 面（拷贝快照；N>0 行降序）。
func Snapshot() []Row {
	reg.Lock()
	defer reg.Unlock()
	out := make([]Row, 0, len(reg.cells))
	for k, c := range reg.cells {
		n := c.n.Load()
		if n == 0 {
			continue
		}
		out = append(out, Row{Key: k, N: n,
			First: time.Unix(0, c.first.Load()), Last: time.Unix(0, c.last.Load())})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Face+out[i].Gate < out[j].Face+out[j].Gate
	})
	return out
}

// Format Δ 期 digest 行（topN；零行=「本期零拒」如实）。
func Format(top int) string {
	rows := Snapshot()
	if len(rows) == 0 {
		return "拒因全景：本期零拒"
	}
	if top > 0 && len(rows) > top {
		rows = rows[:top]
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s.%s ×%d（%s~%s）\n", r.Face, r.Gate, r.N,
			r.First.Format("15:04"), r.Last.Format("15:04"))
	}
	return strings.TrimRight(b.String(), "\n")
}
