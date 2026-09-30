package adapter

import (
	"context"
	"fmt"
	"log"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc/metadata"
)

// reconnectSession 复用现有 conn 重调 connMgr.Connect 重建 account 会话（S1）。
// 禁止调用 a.Connect()——它每次 grpc.DialContext 新建 conn（泄漏）且替换 a.mt5（破坏包装器挂接）。
// 成功仅更新 a.token；sid 由 sessionID(a.user) 确定性生成（网关同 id → 同 token，重连后旧 ctx 仍有效）。
func (a *MT5Adapter) reconnectSession(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sid := sessionID(a.user)
	loginCtx := metadata.NewOutgoingContext(rctx, metadata.New(map[string]string{"id": sid}))
	resp, err := a.connMgr.Connect(loginCtx, &mt5pb.ConnectRequest{
		Host:     a.host,
		Port:     a.port,
		User:     uint64(a.user),
		Password: a.pass,
		Id:       &sid,
	})
	if err != nil {
		return fmt.Errorf("mt5 reconnect: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return fmt.Errorf("mt5 reconnect: %s", e.GetMessage())
	}
	token := resp.GetResult()
	if token == "" {
		return fmt.Errorf("mt5 reconnect: empty token")
	}
	a.token = token
	log.Printf("[mt5:%s] session reconnected, token=%s", a.venue, maskToken(token))
	if a.OnReconnect != nil {
		a.OnReconnect() // D-004 P1#6：对账 kick（须非阻塞——a.mu 持锁中）
	}
	return nil
}

// getToken 持锁读 token（reconnectSession 持 a.mu 写，无锁读=CI 实抓数据竞争）。
func (a *MT5Adapter) getToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token
}

// withSessionMD 给 mtapi 调用附加会话 id header（网关按 id 路由）。
func (a *MT5Adapter) withSessionMD(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"id": a.getToken()}))
}
