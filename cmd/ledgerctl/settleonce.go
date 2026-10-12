// cmd/settleonce D-193-A 扩包返修 R3：settlement 补链微工具。
// 对指定 position 调 settlement.SettleAt（幂等，stats_status 短路），产出真实 settlement 行。
// 桥接=internal/settleadapter（D-508 S13 共享装配单源），elim=lifecycle.EliminatorAdapter。
//
// D-193-A 扩包返修 R3（ZCode 裁定）：479 补走本工具，禁手工 INSERT（方式 B 已否决）。
//
// 用法（复审方执行）：
//
//	NOX_PG_DSN="postgres://nox:nox@nox-postgres:5432/nox?sslmode=disable" \
//	  go run ./cmd/settleonce -pos=479
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"nox/internal/ledger/settleadapter"
	"nox/internal/ledger/settlement"
	"nox/internal/portfolio/lifecycle"
	"nox/internal/store/pgstore"
)

func runSettleOnce() {
	fs := flag.NewFlagSet("settleonce", flag.ExitOnError)
	posID := fs.Int64("pos", 0, "待结算 position ID（必填）")
	fs.Parse(os.Args[2:])
	if *posID <= 0 {
		log.Fatal("-pos 必填（position ID）")
	}
	dsn := os.Getenv("NOX_PG_DSN") // D-145 引导件：连库前必需
	if dsn == "" {
		log.Fatal("NOX_PG_DSN 未设置")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		log.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()

	deps := settleadapter.New(s)

	// 结算前状态
	before, _ := s.GetSettlementStatsStatus(ctx, *posID)
	fmt.Printf("=== D-193-A 扩包 R3：settleonce pos=%d ===\n", *posID)
	fmt.Printf("before: stats_status=%q\n", before)

	// 调 settlement.SettleAt（幂等，elim=lifecycle.EliminatorAdapter，family=lifecycle.FamilyResolverAdapter）
	err = settlement.SettleAt(ctx, deps, *posID, lifecycle.EliminatorAdapter, lifecycle.FamilyResolverAdapter, time.Now())
	if err != nil {
		log.Fatalf("SettleAt pos=%d: %v", *posID, err)
	}

	// 结算后状态
	after, _ := s.GetSettlementStatsStatus(ctx, *posID)
	has, _ := s.HasSettlement(ctx, *posID)
	fmt.Printf("after:  stats_status=%q has_settlement=%v\n", after, has)
	fmt.Println("DONE（质量如实落库，幂等短路=applied/skipped 直接返回）")
}
