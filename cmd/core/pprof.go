// pprof.go 本机诊断面（热修 hotfix-loops）：net/http/pprof 独立 mux 绑定
// 127.0.0.1:6060（NOX_PPROF_ADDR 覆盖；off=关闭）——绝不挂 :50051 公网 mux。
// docker-compose 不暴露该端口；容器内 docker exec … wget localhost:6060/debug/pprof/heap。
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"
)

// startPprof 按 NOX_PPROF_ADDR 起本机 pprof 服务；ctx 取消即关。
// D-145 引导件：debug 通道开关非业务配置——留 env。
func startPprof(ctx context.Context) {
	addr := os.Getenv("NOX_PPROF_ADDR")
	if addr == "off" {
		return
	}
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("[pprof] listen %s: %v", addr, err)
		return
	}
	srv := &http.Server{Handler: http.DefaultServeMux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[pprof] serve: %v", err)
		}
	}()
	log.Printf("[pprof] listening on %s (local only)", addr)
}
