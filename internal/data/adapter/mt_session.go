package adapter

// MT5 网关会话失效自动重连（S1，2026-08-24 立项）。
//
// 背景：core 只在启动时 Connect 一次（mt5.go:81-115），网关侧 RST_STREAM+504 后
// account 级会话失效，所有会话依赖型 RPC 报 .NET `Value cannot be null`，持续挂。
// 复审方直连网关重建 account 会话后 core 同一 gRPC conn 查询静默恢复 → 会话是
// account 级（网关按 id header 路由），重连 = 在同一 conn 上重调 connMgr.Connect。
//
// 设计：sessionClient 包装 mt5pb.MT5Client，仅覆盖 5 个 read 方法（SymbolList /
// SymbolParams / SymbolParamsMany / AccountSummary / OpenedOrders），会话失效时
// 重试一次；写操作不包装（防双发）。reconcile.MT5Querier 共享同一 client，包装后
// 自动受益，零改动。

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sessionLostNeedle 网关侧 .NET ArgumentNullException 的错误串特征（RST_STREAM 后
// account 会话失效时网关返回）。匹配用子串包含，避免依赖 .NET 完整堆栈格式。
const sessionLostNeedle = "Value cannot be null"

// IsSessionLost 分类错误是否为 MT5 网关 account 级会话失效（S1）。
//   - context.DeadlineExceeded / context.Canceled 不算（S5 per-call 超时不得触发重连）；
//   - gRPC status.Code ∈ {Unavailable, Internal} → true（网关层流/服务异常）；
//   - err.Error() 含 "Value cannot be null" → true（网关 .NET 会话失效特征串）；
//   - 普通业务错误（resp.GetError() 消息、其他 .NET 业务异常）→ false。
func IsSessionLost(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable, codes.Internal:
			return true
		}
	}
	if strings.Contains(err.Error(), sessionLostNeedle) {
		return true
	}
	return false
}

// reconnectBackoff 连续重连失败的退避序列（封顶 5m，S1 设计）。
var reconnectBackoff = []time.Duration{
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
}

// sessionClient 包装 mt5pb.MT5Client，对 5 个 read 方法加会话失效检测 + 自动重连。
// 内嵌 mt5pb.MT5Client：未覆盖方法自动透传（与现状等价）。
type sessionClient struct {
	mt5pb.MT5Client
	reconnect func(ctx context.Context) error // 由 MT5Adapter 注入：复用 conn 重调 connMgr.Connect

	mu                  sync.Mutex // 保护失效计数 / 节流 / 退避状态
	failCount           int        // 连续会话失效错误计数
	consecReconnectFail int        // 连续重连失败次数（退避档位索引 +1）
	lastReconnect       time.Time  // 最近一次成功重连时间（节流基准）
	lastAttempt         time.Time  // 最近一次重连尝试时间（退避基准）

	reconnectMu sync.Mutex // 串行重连尝试（不阻塞计数操作）
}

// recordSuccess 重置连续失效计数（成功或非会话失效错误打断连续 streak）。
func (c *sessionClient) recordSuccess() {
	c.mu.Lock()
	c.failCount = 0
	c.mu.Unlock()
}

// maybeReconnect 处理一次会话失效错误：累加计数，达阈值且过节流/退避则触发重连。
// 返回 true = 已重连可重试；false = 未重连（计数不足 / 节流 / 退避 / 重连失败）。
func (c *sessionClient) maybeReconnect(ctx context.Context) bool {
	c.mu.Lock()
	c.failCount++
	if c.failCount < 3 {
		c.mu.Unlock()
		return false
	}
	// 节流：成功重连后 30s 内不再次触发。
	if !c.lastReconnect.IsZero() && time.Since(c.lastReconnect) < 30*time.Second {
		c.mu.Unlock()
		return false
	}
	// 退避：连续重连失败后按序列等待。
	if c.consecReconnectFail > 0 {
		idx := c.consecReconnectFail - 1
		if idx >= len(reconnectBackoff) {
			idx = len(reconnectBackoff) - 1
		}
		if !c.lastAttempt.IsZero() && time.Since(c.lastAttempt) < reconnectBackoff[idx] {
			c.mu.Unlock()
			return false
		}
	}
	c.mu.Unlock()

	// 串行重连（reconnectMu），不持 c.mu 以免阻塞计数操作。
	c.reconnectMu.Lock()
	defer c.reconnectMu.Unlock()

	// double-check：另一 goroutine 可能刚重连成功。
	c.mu.Lock()
	if !c.lastReconnect.IsZero() && time.Since(c.lastReconnect) < 30*time.Second {
		c.mu.Unlock()
		return false
	}
	c.lastAttempt = time.Now()
	c.mu.Unlock()

	err := c.reconnect(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.consecReconnectFail++ // 失败 → 计数不清，进位退避
		return false
	}
	c.failCount = 0
	c.consecReconnectFail = 0
	c.lastReconnect = time.Now()
	return true
}

// wrapCall 通用包装：调用 inner，会话失效则重连后重试一次。
func wrapCall[Req any, Resp any](
	c *sessionClient,
	ctx context.Context,
	in *Req,
	opts []grpc.CallOption,
	fn func(ctx context.Context, in *Req, opts ...grpc.CallOption) (*Resp, error),
) (*Resp, error) {
	resp, err := fn(ctx, in, opts...)
	if err == nil || !IsSessionLost(err) {
		if err == nil {
			c.recordSuccess()
		}
		return resp, err
	}
	if c.maybeReconnect(ctx) {
		return fn(ctx, in, opts...)
	}
	return resp, err
}

// 5 个 read 方法覆盖（其余透传内嵌实现）。
// 注意：opts 透传以保留调用方注入的 metadata / 超时等 CallOption。

func (c *sessionClient) SymbolList(ctx context.Context, in *mt5pb.SymbolListRequest, opts ...grpc.CallOption) (*mt5pb.SymbolListReply, error) {
	return wrapCall(c, ctx, in, opts, c.MT5Client.SymbolList)
}

func (c *sessionClient) SymbolParams(ctx context.Context, in *mt5pb.SymbolParamsRequest, opts ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	return wrapCall(c, ctx, in, opts, c.MT5Client.SymbolParams)
}

func (c *sessionClient) SymbolParamsMany(ctx context.Context, in *mt5pb.SymbolParamsManyRequest, opts ...grpc.CallOption) (*mt5pb.SymbolParamsManyReply, error) {
	return wrapCall(c, ctx, in, opts, c.MT5Client.SymbolParamsMany)
}

func (c *sessionClient) AccountSummary(ctx context.Context, in *mt5pb.AccountSummaryRequest, opts ...grpc.CallOption) (*mt5pb.AccountSummaryReply, error) {
	return wrapCall(c, ctx, in, opts, c.MT5Client.AccountSummary)
}

func (c *sessionClient) OpenedOrders(ctx context.Context, in *mt5pb.OpenedOrdersRequest, opts ...grpc.CallOption) (*mt5pb.OpenedOrdersReply, error) {
	return wrapCall(c, ctx, in, opts, c.MT5Client.OpenedOrders)
}

// sessionID/maskToken 原定义于已删 mt4.go，D-470 上移为 MT5 侧 helper。
func sessionID(user int64) string {
	return "mdgw-" + strconv.FormatInt(user, 10)
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "..." + t[len(t)-4:]
}
