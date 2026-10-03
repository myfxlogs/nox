package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/adapter"
	"arb/internal/fact"
)

// SwapFacts 聚合全部运行中/静态适配器的 swap facts（§3.6）。
// 一个都拿不到时：DB 中还有应运行的账号（status=connected）→ 返回错误（Scheduler 退避重试直到适配器连上，
// 覆盖启动竞态：scheduler 首轮早于适配器连上，§7.4）；无 CFD 部署 / 全部移除·停用 → 返回空，按周期等待不制造噪声。
func (m *BrokerManager) SwapFacts(ctx context.Context) ([]fact.Fact, error) {
	m.mu.Lock()
	var sources int
	swapFns := make([]func(context.Context) ([]fact.Fact, error), 0, len(m.running)+len(m.static))
	for _, r := range m.running {
		if r.swap != nil {
			sources++
			swapFns = append(swapFns, r.swap)
		}
	}
	for _, s := range m.static {
		if s.swap != nil {
			sources++
			swapFns = append(swapFns, s.swap)
		}
	}
	m.mu.Unlock()

	if sources == 0 {
		if m.hasStartableAccounts(ctx) {
			return nil, fmt.Errorf("no swap sources (adapters connecting?)")
		}
		return nil, nil
	}

	var facts []fact.Fact
	var errs []string
	hasCold := false
	for _, fn := range swapFns {
		fs, err := fn(ctx)
		// D-104 P0 返修③ R-c1：SwapFacts 冷符号时 facts 与 ErrSwapColdStart 同返——
		// 先 append facts 再 continue，冷符号等下一轮补，其余 facts 立即落地。
		if fs != nil {
			facts = append(facts, fs...)
		}
		if err != nil {
			if errors.Is(err, adapter.ErrSwapColdStart) {
				hasCold = true
			}
			errs = append(errs, err.Error())
			continue
		}
	}
	m.mu.Lock()
	retryCold := false
	if hasCold {
		if m.coldStartSince.IsZero() {
			m.coldStartSince = time.Now()
		}
		retryCold = time.Since(m.coldStartSince) < coldStartBound
	} else {
		m.coldStartSince = time.Time{}
	}
	m.mu.Unlock()
	if hasCold && retryCold {
		return nil, fmt.Errorf("swap cold start: %s", strings.Join(errs, "; "))
	}
	if len(facts) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("swap aggregate: %s", strings.Join(errs, "; "))
	}
	for _, e := range errs {
		log.Printf("[broker] swap source: %v", e)
	}
	return facts, nil
}

// coldStartBound 冷启动退避窗口：期间任一 venue 缺报价即整轮重试；超窗接受部分，等下一轮补全。
const coldStartBound = 60 * time.Second

// MetaFacts 聚合全部运行中适配器的符号元数据 facts（D-140 S2）。
// best-effort：单 venue 失败跳过不阻塞；无 MT5 源 → 返回空 nil。
func (m *BrokerManager) MetaFacts(ctx context.Context) ([]fact.Fact, error) {
	m.mu.Lock()
	metaFns := make([]func(context.Context) ([]fact.Fact, error), 0, len(m.running))
	for _, r := range m.running {
		if r.meta != nil {
			metaFns = append(metaFns, r.meta)
		}
	}
	m.mu.Unlock()
	if len(metaFns) == 0 {
		return nil, nil
	}
	var facts []fact.Fact
	for _, fn := range metaFns {
		fs, err := fn(ctx)
		if err != nil {
			log.Printf("[broker] meta source: %v", err)
			continue
		}
		facts = append(facts, fs...)
	}
	return facts, nil
}

// hasStartableAccounts 是否还有应运行的 CFD 账号（status=connected，StartAll 的启动谓词）。
// 查询失败保守视为有——启动竞态（scheduler 首轮早于适配器连上）宁可退避重试，不可静默吞掉。
func (m *BrokerManager) hasStartableAccounts(ctx context.Context) bool {
	accounts, err := m.store.ListBrokerAccounts(ctx)
	if err != nil {
		log.Printf("[broker] hasStartableAccounts: %v", err)
		return true
	}
	for _, a := range accounts {
		if a.Status == "connected" {
			return true
		}
	}
	return false
}

// SessionsFacts D-029 ①：聚合全部静态适配器的周交易会话表 facts（单 venue 部署=直通）。
// 一个都拿不到 → 空表（session 采集缺席=engine 判定回退动态径/10018 兜底，不制造噪声）。
func (m *BrokerManager) SessionsFacts(ctx context.Context) ([]fact.Fact, error) {
	// D-029 R3：聚合 running+static（同 SwapFacts 覆盖面——running=sessions 字段经
	// broker_manager_adapters.go 注册 ad.SessionsFacts；static=AddStaticSessionsSource）。
	m.mu.Lock()
	fns := make([]func(context.Context) ([]fact.Fact, error), 0, len(m.static)+len(m.running))
	for _, s := range m.static {
		if s.sessions != nil {
			fns = append(fns, s.sessions)
		}
	}
	for _, r := range m.running {
		if r.sessions != nil {
			fns = append(fns, r.sessions)
		}
	}
	m.mu.Unlock()
	var out []fact.Fact
	for i, fn := range fns {
		fs, err := fn(ctx)
		if err != nil {
			// D-029 R2 ②：单源失败 log.Printf 不吞（仍继续其余源）——原静默 continue 使
			// 「周表空 boot 竞态」不可见（R1 首拍竞态复核发现）。
			log.Printf("[broker] SessionsFacts source %d: %v", i, err)
			continue
		}
		out = append(out, fs...)
	}
	return out, nil
}

// RequiredMarginFor D-031 ②：venueKey 对应 running 适配器的仓位级原生保证金 RPC
// （RequiredMargin(symbol, lots, DealBuy/DealSell, openPrice)）。venue 无 running 适配器
// /RPC 失败 → ok=false（调用方诚实空态，不缓存失败值）。
func (m *BrokerManager) RequiredMarginFor(ctx context.Context, venueKey, symbol, side string, lots, price float64) (float64, bool) {
	m.mu.Lock()
	var fn func(ctx context.Context, symbol, side string, lots, price float64) (float64, error)
	for v, r := range m.running {
		if r.requiredMargin != nil && (venueKey == v || venueKey == v+":mt5" || venueKey == v+":mt4") {
			fn = r.requiredMargin
			break
		}
	}
	m.mu.Unlock()
	if fn == nil {
		return 0, false
	}
	val, err := fn(ctx, symbol, side, lots, price)
	if err != nil {
		log.Printf("[broker] required margin %s %s: %v", venueKey, symbol, err)
		return 0, false
	}
	return val, true
}
