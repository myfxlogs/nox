package adapter

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// dialContext 可替换的 grpc.DialContext（D-104 P2 U1 测试用，同 marginReconnectBackoff 包级 var 模式）。
var dialContext = func(ctx context.Context, target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return grpc.DialContext(ctx, target, opts...)
}

// defaultDialOpts MT 网关通用 dial 选项（TLS1.3 + keepalive + block）。
func defaultDialOpts() []grpc.DialOption {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	return []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
}

// dialGateways 按序试 targets（[gateway] + fallbacks），成功即用，全败返最后 err（D-104 P2 U1）。
// kind = "mt5"/"mt4" 用于日志；非首节点成功时记 fallback 日志。
func dialGateways(ctx context.Context, kind, venue string, targets []string) (*grpc.ClientConn, error) {
	opts := defaultDialOpts()
	var lastErr error
	for i, target := range targets {
		conn, err := dialContext(ctx, target, opts...)
		if err == nil {
			if i > 0 {
				log.Printf("[%s:%s] connected via fallback %s", kind, venue, target)
			}
			return conn, nil
		}
		lastErr = err
		log.Printf("[%s:%s] dial %s: %v, trying next", kind, venue, target, err)
	}
	return nil, fmt.Errorf("%s dial: all gateways failed: %w", kind, lastErr)
}
