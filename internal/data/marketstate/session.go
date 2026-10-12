// session.go —— D-170：trade_session 周表装载/判定（从 autotrade/engine_session.go
// 提取共享件——MT 腿静默裁决与引擎休市判定同一份装载逻辑，防双轨漂移）。
// 口径：adapter 落库已换算服务器→UTC；`sym#n` 计数行界清有效窗数，idx≥count 幽灵窗
// 丢弃（同 canonical 多 brokerSym 历史并写不被覆盖删除）。
package marketstate

import (
	"context"
	"strconv"
	"strings"
	"time"

	"arb/internal/data/fact"
)

// Window 周会话窗（UTC 坐标）：wday=UTC 周几（起点日），start/end=UTC 分钟
// （跨午夜 end<start 判定 wrap；start=end=0=全天窗）。
type Window struct {
	Wday     int
	StartMin int
	EndMin   int
}

// InSession now 是否落在该会话内（跨午夜：起点日尾段 ∨ 次日头段）。
// start=end=0 是全天窗——MT5 end=1440 经 toUTCSession 折成 0
// （247m 类符号每交易日一行 00:00-24:00），不能走 end>=start 普通分支。
func (w Window) InSession(now time.Time) bool {
	nm := now.UTC().Hour()*60 + now.UTC().Minute()
	nd := int(now.UTC().Weekday())
	if w.StartMin == 0 && w.EndMin == 0 {
		return nd == w.Wday // 全天窗：当天任意分钟都在市
	}
	if w.EndMin >= w.StartMin {
		return nd == w.Wday && nm >= w.StartMin && nm <= w.EndMin
	}
	if nd == w.Wday && nm >= w.StartMin {
		return true
	}
	return nd == (w.Wday+1)%7 && nm <= w.EndMin
}

// Table MT 腿周会话表（UTC）：key=`venue|symbol`（bare venue + canonical）。
type Table map[string][]Window

// SessionSource 周表数据源（*pgstore.Store 天然实现）。
type SessionSource interface {
	LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error)
}

// LoadSessionTable 全量拉 trade_session facts 组表（行数=符号×7×session，百级内）。
// facts 空/查询错 → 空表（调用方按「表缺席」语义处理，不在此 fail）。
func LoadSessionTable(ctx context.Context, src SessionSource) Table {
	facts, err := src.LatestFactsByKind(ctx, fact.KindTradeSession)
	if err != nil || len(facts) == 0 {
		return Table{}
	}
	type widx struct {
		idx int
		w   Window
	}
	counts := map[string]int{}
	raw := map[string][]widx{}
	for _, f := range facts {
		sym := f.Symbol
		idx := -1
		if i := strings.Index(sym, "#"); i >= 0 {
			suffix := sym[i+1:]
			sym = sym[:i] // D-029：adapter 唯一化后缀（canon#idx）剥离
			if suffix == "n" {
				counts[f.Venue+"|"+sym] = int(f.Value)
				continue // 计数行不入窗
			}
			if n, aerr := strconv.Atoi(suffix); aerr == nil {
				idx = n
			}
		}
		v := int64(f.Value)
		key := f.Venue + "|" + sym
		raw[key] = append(raw[key], widx{idx: idx, w: Window{
			Wday: int(v / 1e8), StartMin: int(v / 1e4 % 1e4), EndMin: int(v % 1e4),
		}})
	}
	t := Table{}
	for k, ws := range raw {
		cnt, declared := counts[k]
		for _, e := range ws {
			if declared && e.idx >= cnt {
				continue // 幽灵窗（残留旧符号行的越界序号）
			}
			t[k] = append(t[k], e.w)
		}
	}
	return t
}

// OpenAt key=venue|canonical 在 now 是否开市；known=false=表缺席（无该键证据）。
func (t Table) OpenAt(venue, symbol string, now time.Time) (open, known bool) {
	win, ok := t[venue+"|"+symbol]
	if !ok {
		return false, false
	}
	for _, w := range win {
		if w.InSession(now) {
			return true, true
		}
	}
	return false, true
}
