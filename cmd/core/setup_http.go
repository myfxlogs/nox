// setup_http.go —— §9 HTTP 服务装配 + 阻塞运行 + 优雅关闭（自 setup.go 拆出保行数；
// setup_readyz.go 同先例，纯搬家零语义）。
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nox/internal/data/bus"
	"nox/internal/ir/notify"
	"nox/internal/platform/analyst"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"

	"nox/internal/trading/exec"
	"nox/web"
)

// serveHTTP 装配 mux（/health /readyz Dashboard handler /webhook/feishu 静态 web）并阻塞至
// SIGINT/SIGTERM 后优雅关闭（原 runServices §9 段）。
func serveHTTP(cancel context.CancelFunc, cfg *config.Config, st *pgstore.Store, qbus *bus.QuoteBus, execReg *exec.Registry, liveGateChecker *gate.LiveGateChecker, feishuClient *notify.FeishuClient, secRes *config.SecretResolver, dsn, path string, handler http.Handler, probeDecider notify.ProbeDecider, dciRunner notify.DCIGoRunner) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// D-159 W6a S4 + D-195-C1 S3：/readyz readiness 8 层分级端点（liveness=/health 保留）。
	// JSON：8 层 ReadinessReport + tradable。DB fail → 503；其余 degraded 仍 200。
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := readyzCheck(r.Context(), st, qbus, cfg, execReg, liveGateChecker)
		code := http.StatusOK
		if !resp.DatabaseReady.Ready {
			code = http.StatusServiceUnavailable
		}
		w.WriteHeader(code)
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	})
	mux.Handle(path, handler)
	// D-264：飞书事件订阅回调（challenge 握手+首条消息绑定 chat_id；公开端点仅提取无动作）。
	// D-270 S3：扩展 replyHandler + feishuClient（已绑定→解析回复指令→SendText 发确认回执）。
	// D-284 S4 → D-139：ReplyRouter 随 repo 面摘除（OK/SKIP 指令面退役），直连 FeishuQueryHandler。
	if cfg.FeishuAppID != "" {
		queryHandler := notify.NewFeishuQueryHandler(
			&notify.PgStoreQueryAdapter{Store: st},
			st, // *pgstore.Store 实现 AISettingsReader（GetAiSettings 已存在）
			feishuClient,
			cfg.AIBaseURL, cfg.AIModel, cfg.AIAPIKey,
			analyst.NewRateLimiter(cfg.AnalystRatePerHour), // 独立限流器，与前端分析员分开计数
		)
		// D-176②：飞书「批准#N/拒绝#N」口令 → 与前端按钮同一 RPC 语义。
		queryHandler.SetProbeDecider(probeDecider)
		// D-191②：飞书「go」口令 → DCI covered-call 申购执行（request_id 幂等）。
		queryHandler.SetDCIGoRunner(dciRunner)
		// D-194：「提案」口令 → pending 提案清单（store 直装同型）。
		queryHandler.SetProposalLister(st)
		mux.Handle("/webhook/feishu", notify.FeishuWebhookHandler(st, secRes.Get("NOX_FEISHU_VERIFY_TOKEN"), feishuClient, queryHandler))
	}
	mux.Handle("/", web.Handler(os.Getenv("NOX_WEB_DIR"))) // D-145 引导件：embed 覆盖开发调试径，非业务配置

	srv := &http.Server{
		Addr:    ":50051",
		Handler: mux,
	}

	safego.GoLoop("setup:httpServer", func() {
		log.Printf("core listening on %s (dsn=%s)", srv.Addr, maskDSN(dsn))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	})

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Println("core stopped")
}
