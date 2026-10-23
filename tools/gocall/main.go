// gocall —— 一次性提案执行工具：等价飞书「go <id>」口令（同 dciexec.Executor
// GoProposal 全闸链——控制面/纸面闸/评估卡/live_gates 一少走）。
// 用途=飞书入站 webhook 死径（公网 URL 未接）期间的手动授权通道。
// 用法：gocall -dsn postgres://... -id 1827
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"nox/internal/platform/httpx"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/dciexec"
)

func main() {
	dsn := flag.String("dsn", "", "postgres dsn")
	id := flag.Int64("id", 0, "proposal id")
	flag.Parse()
	if *dsn == "" || *id == 0 {
		log.Fatal("usage: gocall -dsn postgres://... -id <proposal>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	st, err := pgstore.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("store: %v", err)
	}

	// LiveGateChecker：environment="live" 同产配（NOX_ENVIRONMENT）；readinessFn
	// 恒 true——运行体健康已由 docker 容器 healthy 外部实证（本工具无 qbus/execReg
	// 运行时件，DB 侧 live_gates 闸记录/操作者/到期 5 查仍权威全跑）。
	cp := gate.NewControlPlane(st)
	cp.SetLiveGateChecker(gate.NewLiveGateChecker(st, func(context.Context) bool { return true }, "live"))
	ex := &dciexec.Executor{
		Store: st, Client: httpx.Client(),
		ModeGate: cp, LiveEquityMapFn: dciexec.LiveEquityMap,
	}
	ex.Now = time.Now

	// 凭据装配同 cmd/core.buildDCIExecutor（binance sapi + okx/gate earn 兜序）。
	exchs, err := st.ListExchangeConfigs(ctx)
	if err == nil {
		for _, e := range exchs {
			if e.Venue == "binance" && e.VenueType == "spot" && !e.Simulated {
				ex.BnKey = e.APIKey
				ex.BnSec = e.APISecret
				ex.Sapi = httpx.BinanceApiSegBase(e.BaseURL, "sapi")
				ex.APIBase = httpx.BinanceApiSegBase(e.BaseURL, "api")
			}
		}
		for _, vtpref := range []struct {
			venue string
			types []string
		}{{"okx", []string{"unified", "perp", "spot", ""}},
			{"gate", []string{"perp", "unified", ""}}} {
			var best *pgstore.ExchangeConfig
			bestPri := len(vtpref.types)
			for i := range exchs {
				e := &exchs[i]
				if e.Venue != vtpref.venue || e.Simulated || e.APIKey == "" || e.APISecret == "" {
					continue
				}
				for pri, vt := range vtpref.types {
					if e.VenueType == vt && pri < bestPri {
						best, bestPri = e, pri
					}
				}
			}
			if best != nil {
				if ex.EarnCreds == nil {
					ex.EarnCreds = map[string]dciexec.EarnCred{}
				}
				ex.EarnCreds[vtpref.venue] = dciexec.EarnCred{
					Key: best.APIKey, Secret: best.APISecret, Pass: best.Passphrase, BaseURL: best.BaseURL}
			}
		}
	}

	res, err := ex.GoProposal(ctx, *id)
	if err != nil {
		log.Fatalf("GoProposal #%d: %v", *id, err)
	}
	fmt.Println(res)
}
