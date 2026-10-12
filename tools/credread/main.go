// tools/credread —— D-564 S5 U7：venue 凭据 pg 只读出口（脚本域消费面）。
// 从 exchange_configs 解密读指定 venue（real 域，simulated=false）的凭据，
// 以 KEY=VALUE 行输出到 stdout（供 bash eval 注入；**勿落日志勿回显**）。
// 凭据权威=exchange_configs 密文列 + NOX_MASTER_KEY（D-515 轨）；env 已退役——
// 本工具失败=fail-closed 报「凭据在 exchange_configs」，调用方不得回落 env。
//
// 用法：credread -venue okx [-type perp] [-field api_key|api_secret|passphrase|base_url|all]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"nox/internal/store/pgstore"
)

func main() {
	venue := flag.String("venue", "", "目标 venue（okx/gate/binance…；必填）")
	vType := flag.String("type", "", "venue_type 过滤（perp/spot…；空=该 venue 任一 real 行）")
	field := flag.String("field", "all", "输出字段（api_key/api_secret/passphrase/base_url/all）")
	flag.Parse()
	if *venue == "" {
		log.Fatal("[credread] -venue 必填")
	}
	dsn := os.Getenv("DB_DSN") // D-145 引导件：连库前必需
	if dsn == "" {
		log.Fatal("[credread] DB_DSN 未设置（凭据在 exchange_configs，env 已退役——fail-closed）")
	}
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("[credread] pg connect: %v", err)
	}
	defer st.Close()
	cfgs, err := st.ListExchangeConfigs(context.Background())
	if err != nil {
		log.Fatalf("[credread] exchange_configs 解密读（需 NOX_MASTER_KEY）: %v", err)
	}
	for _, c := range cfgs {
		if c.Venue != *venue || c.Simulated {
			continue
		}
		if *vType != "" && c.VenueType != *vType {
			continue
		}
		emit := func(k, v string) {
			if v != "" {
				fmt.Printf("%s=%s\n", k, v)
			}
		}
		switch strings.ToLower(*field) {
		case "api_key":
			emit("API_KEY", c.APIKey)
		case "api_secret":
			emit("API_SECRET", c.APISecret)
		case "passphrase":
			emit("PASSPHRASE", c.Passphrase)
		case "base_url":
			emit("BASE_URL", c.BaseURL)
		default:
			emit("API_KEY", c.APIKey)
			emit("API_SECRET", c.APISecret)
			emit("PASSPHRASE", c.Passphrase)
			emit("BASE_URL", c.BaseURL)
		}
		return
	}
	log.Fatalf("[credread] venue=%s type=%s 无 real 凭据行（fail-closed——env 已退役，检查 exchange_configs）", *venue, *vType)
}
