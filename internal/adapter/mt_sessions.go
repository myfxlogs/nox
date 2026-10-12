// mt_sessions.go —— D-029 ①：MT5 周交易会话表采集（SymbolSessionsEx，应用层首用）。
// 每符号一次 RPC（Trades 数组下标=服务器本地周几 0=Sun..6=Sat；Sessions=当日时段，分钟）。
// 服务器时区经 ServerTimezone RPC 实取（offset 小时）→ 会话换算成 **UTC 坐标**落库
// （value=utcWday*1e8+utcStartMin*1e4+utcEndMin；跨午夜
// end<start 允许，判定侧 wrap）。单 offset 全周假设=DST 切换日 ±1h 误差（披露：动态
// 新鲜度径兜底）。每符号×每 session 一行 fact（kind=trade_session，canonical+native 双发
// 同 trade_mode 惯例）。表为静态周表：adapter 每日刷新一次（Scheduler Named 24h）。
// 有界并发 8 / per-call 3s 超时 / token 会话 MD——同 SwapFacts 惯例。
package adapter

import (
	"context"
	"strconv"
	"sync"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/fact"
)

const sessionsPerCallTimeout = 3 * time.Second

// SessionsFacts 拉全部已映射符号的周会话表，归一为 trade_session facts。
// 单符号失败只记日志；全失败才返回错误（Scheduler 退避重试）。
// D-083：canonical 标签发射闸同 mt_swap.go D-383 A2.3——blocked[brokerSym]
// 只发 native 不发 canonical（修 XAUUSDm/XAUUSD247m 同 canonical 同轮并写、
// LatestFacts 后写赢者随机翻转 XAUUSD 周表的撞车）；并每符号发 `#n` 窗计数行
// 供读侧丢弃幽灵窗（被 blocked 符号残留的旧 canonical 行 idx 可能超出真实窗数）。
func (a *MT5Adapter) SessionsFacts(ctx context.Context) ([]fact.Fact, error) {
	sm := a.SymbolMap()
	if len(sm) == 0 {
		return nil, nil
	}
	blocked := a.emitBlockedSet() // D-083：单次快照（同 SwapFacts 并发风格）
	// 服务器时区偏移实取（fail-closed：取不到偏移无法换算 UTC=本采集轮放弃，下轮重试）。
	tctx, tcancel := context.WithTimeout(a.withSessionMD(ctx), 5*time.Second)
	defer tcancel()
	tzResp, terr := a.mt5.ServerTimezone(tctx, &mt5pb.ServerTimezoneRequest{Id: a.getToken()})
	if terr != nil {
		return nil, context.DeadlineExceeded // Scheduler 退避重试（换算缺偏移不可产出）
	}
	if e := tzResp.GetError(); e != nil {
		return nil, context.DeadlineExceeded
	}
	offHours := tzResp.GetResult()
	if offHours < -14 || offHours > 14 {
		return nil, context.DeadlineExceeded
	}
	srvLoc := time.FixedZone("srv", int(offHours*3600))
	srvNow := time.Now().In(srvLoc)

	type symEntry struct{ brokerSym, canon string }
	syms := make([]symEntry, 0, len(sm))
	for brokerSym, canon := range sm {
		syms = append(syms, symEntry{brokerSym, canon})
	}
	slots := make([][]fact.Fact, len(syms))
	errs := make([]string, len(syms))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	tok := a.getToken()
	for i, ent := range syms {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, brokerSym string) {
			defer wg.Done()
			<-sem
			pctx, cancel := context.WithTimeout(a.withSessionMD(ctx), sessionsPerCallTimeout)
			resp, err := a.mt5.SymbolSessionsEx(pctx, &mt5pb.SymbolSessionsExRequest{Id: tok, Symbol: brokerSym})
			cancel()
			if err != nil {
				errs[idx] = brokerSym + ": " + err.Error()
				return
			}
			if e := resp.GetError(); e != nil {
				errs[idx] = brokerSym + ": " + e.GetMessage()
				return
			}
			r := resp.GetResult()
			if r == nil {
				errs[idx] = brokerSym + ": result nil"
				return
			}
			slots[idx] = sessionFactsFor(a.venue, syms[idx].canon, brokerSym, r.GetTrades(), srvLoc, srvNow, blocked)
		}(i, ent.brokerSym)
	}
	wg.Wait()
	var out []fact.Fact
	fail := 0
	for i := range slots {
		if len(errs[i]) > 0 {
			fail++
			if fail <= 3 {
				logSessionErr(errs[i])
			}
			continue
		}
		out = append(out, slots[i]...)
	}
	if fail == len(syms) {
		return nil, context.DeadlineExceeded
	}
	return out, nil
}

// sessionFactsFor 单符号周表 → trade_session facts（canonical+native 双发）。
// day=Trades 数组下标（服务器本地 0=Sun..6=Sat）；换算：以 srvNow 为锚找该 weekday
// 最近一次出现日 → session 起止（服务器本地）→ 绝对 UTC → 编码（判定侧纯 UTC）。
// D-083：blocked[brokerSym] 时只发 native 不发 canonical（D-383 A2.3 同语义——
// 多行组非 primary 行仅停 canonical 标签）；每符号附 `symbol#n` 计数行（Value=窗数，
// 读侧据此丢弃被压行的旧符号残留幽灵窗）。blocked 空集=全发（旧行为）。
func sessionFactsFor(venue, canon, brokerSym string, days []*mt5pb.SessionsForDay, srvLoc *time.Location, srvNow time.Time, blocked map[string]bool) []fact.Fact {
	// D-029：symbol 唯一化 canon#idx——LatestFactsByKind 按 (venue,symbol) 折叠取最新，
	// 同 symbol 多窗行会互吞；idx 全局递增保每窗一行。读侧解析取 "#" 前缀。
	emit := func(symbol string) []fact.Fact {
		var out []fact.Fact
		idx := 0
		for day := range days {
			for _, sess := range days[day].GetSessions() {
				sm, em := int(sess.GetStartTime()), int(sess.GetEndTime())
				if sm < 0 || em < 0 || sm >= 1440 || em > 1440 {
					continue
				}
				utcW, utcSm, utcEm := toUTCSession(srvLoc, srvNow, day, sm, em)
				out = append(out, fact.Fact{
					Kind:   fact.KindTradeSession,
					Venue:  venue,
					Symbol: symbol + "#" + strconv.Itoa(idx),
					Value:  float64(utcW*1e8 + utcSm*1e4 + utcEm), // day*1e7+start*1e4+end（start/end≤1439<1e4 无位污染；≪2^53）
					Unit:   "utc_session",
					Ts:     time.Now(),
				})
				idx++
			}
		}
		// D-083：窗计数行——同 canonical 另一符号的旧窗行不会被本符号覆盖删除，
		// 读侧以 n 截断丢弃 idx≥n 的残留（无 #n 行=旧数据不过滤，兼容）。
		out = append(out, fact.Fact{
			Kind:   fact.KindTradeSession,
			Venue:  venue,
			Symbol: symbol + "#n",
			Value:  float64(idx),
			Unit:   "utc_session_count",
			Ts:     time.Now(),
		})
		return out
	}
	var out []fact.Fact
	if !blocked[brokerSym] { // D-383 A2.3 同闸：canonical 标签停发，native 照发
		out = emit(canon)
	}
	if brokerSym != canon {
		out = append(out, emit(brokerSym)...)
	}
	return out
}

// toUTCSession 服务器本地 (day,startMin,endMin) → 最近出现的绝对时刻 → UTC 编码。
// end<start=跨午夜：end 取次日分钟（编码保原分钟值，wrap 判定在读侧）。
func toUTCSession(srvLoc *time.Location, srvNow time.Time, day, startMin, endMin int) (wUTC, startUTCMin, endUTCMin int) {
	base := srvNow.AddDate(0, 0, -7)
	for k := 0; k < 16; k++ { // ±7 天窗内首个匹配 weekday 的日
		d := base.AddDate(0, 0, k)
		if int(d.Weekday()) != day {
			continue
		}
		startUTC := time.Date(d.Year(), d.Month(), d.Day(), startMin/60, startMin%60, 0, 0, srvLoc).UTC()
		endAbs := startUTC.Add(time.Duration(endMin-startMin) * time.Minute) // 跨午夜自然 +1d
		return int(startUTC.Weekday()), startUTC.Hour()*60 + startUTC.Minute(), endAbs.Hour()*60 + endAbs.Minute()
	}
	return day, startMin, endMin // 窗外（不可达：±7 天必覆盖任一 weekday）
}
