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
