// service_overview_truenet.go —— D-636：true_net 段 TTL 缓存（MT5 接入层恢复票 823 的定位修复件）。
// 定位实证（SG 10-20 三通道）：GetOverviewBundle 每请求重跑 TrueNetProfitSince 双窗
// （14 池×2 拍+2 io=31 点查，单查 71ms、覆盖索引在案）×UI 轮询=266 段/6h；pgxpool
// MaxConns=10 被 errgroup 12+段并发+轮询吃满→段内逐查获取排队→整段 2m5s 束死，
// 同窗 5s 预算读者（leg unit qty/funding fact/earn_yield）全超=「开仓径三堵之二」
// 残面（leg RPC 超时）的真身=PG 池饿，非网关进程（mtapi=mt5grpc*.mtapi.io 托管服务
// 无本地进程可重启，adapter 断线自愈在案——skills/mt5-gateway-admin）。
// 缓存语义：TTL 内直用缓存；过期由单赢家（busy 闸）同步刷，并发请求吃 stale 不排队
// 不放大；首启无值且非赢家=如实缺（available=false 原读败语义）。刷新失败=缓存不动
// 保 stale。D-604 合法域：显示面历史台账聚合数（非决策路径、非 venue 状态读）。
package dashboard

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// trueNetCacheTTL 段缓存租期（30d/全时权威口径显示面——5min 粒度足）。
const trueNetCacheTTL = 5 * time.Minute

// trueNetRefreshTimeout 单次重算上限（去耦 bundle 请求寿命；池饿复发时保缓存轨存活）。
const trueNetRefreshTimeout = 2 * time.Minute

// trueNetView 缓存视图（resp 直填面）。
type trueNetView struct {
	n30, nAll float64
	covered   int
	uncovered string
}

// trueNetCache 段缓存（Service 单例域；busy=单赢家重算闸）。
type trueNetCache struct {
	mu    sync.Mutex
	view  *trueNetView
	at    time.Time
	busy  bool
	fetch func(ctx context.Context, since, until time.Time) (*pgstore.TrueNetProfit, error) // 测试注 counting fake；nil=store 真径
}

// trueNetSeg 总览 §12 段（原内联体迁此+缓存化——prefetch 每请求直填零重算）。
func (s *Service) trueNetSeg(resp *v1.GetOverviewBundleResponse, now time.Time) {
	c := &s.tnCache
	c.mu.Lock()
	fresh := c.view != nil && now.Sub(c.at) < trueNetCacheTTL
	if !fresh && !c.busy {
		c.busy = true
		c.mu.Unlock()
		s.trueNetRefresh(now)
		c.mu.Lock()
		c.busy = false
	}
	view := c.view
	c.mu.Unlock()
	if view == nil {
		return // 首启无值且非赢家=如实缺
	}
	resp.TrueNet_30DUsd = view.n30
	resp.TrueNetAllUsd = view.nAll
	resp.TrueNetAvailable = view.covered > 0
	resp.TrueNetUncovered = view.uncovered
}

// trueNetRefresh 重算（原 §12 内联查询域；双窗全成才覆缓存——原「任一读败整段缺」语义保真）。
func (s *Service) trueNetRefresh(now time.Time) {
	fetch := s.tnCache.fetch
	if fetch == nil {
		fetch = s.store.TrueNetProfitSince
	}
	ctx, cancel := context.WithTimeout(context.Background(), trueNetRefreshTimeout)
	defer cancel()
	r30, err := fetch(ctx, now.Add(-30*24*time.Hour), now)
	if err != nil {
		log.Printf("[overview] true_net refresh 30d: %v (缓存 stale 保)", err)
		return
	}
	rall, aerr := fetch(ctx, time.Unix(0, 0), now)
	if aerr != nil {
		log.Printf("[overview] true_net refresh all: %v (缓存 stale 保)", aerr)
		return
	}
	unc := append(append([]string{}, r30.Uncovered...), rall.Uncovered...)
	for _, v := range rall.IoGap {
		unc = append(unc, v+":io采集缺") // io 缺口 venue 如实标（gate 实证面）
	}
	if len(unc) > 8 {
		unc = unc[:8]
	}
	s.tnCache.mu.Lock()
	s.tnCache.view = &trueNetView{
		n30:       r30.NetUSD,
		nAll:      rall.NetUSD,
		covered:   len(rall.Covered),
		uncovered: strings.Join(unc, ";"),
	}
	s.tnCache.at = now
	s.tnCache.mu.Unlock()
}
