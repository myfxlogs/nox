// tools/alert-emit —— 宿主侧告警摄入件（cron/systemd 脚本用——
// 走 pgstore.InsertAlertDedup 摄入端点，禁裸 SQL INSERT 径）。
//
// 用法：alert-emit -level warn|critical -msg "…" [-dedup 24h]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"nox/internal/store/pgstore"
)

func main() {
	level := flag.String("level", "warn", "info|warn|critical")
	msg := flag.String("msg", "", "告警文案（禁携业务数据——D-004 看门狗语义）")
	dedup := flag.Duration("dedup", 24*time.Hour, "同文案去重窗")
	flag.Parse()
	if *msg == "" {
		log.Fatal("-msg 必传")
	}
	dsn := os.Getenv("NOX_PG_DSN")
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		log.Fatal("NOX_PG_DSN/DB_DSN 未设")
	}
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer st.Close()
	if err := st.InsertAlertDedup(ctx, *level, *msg, *dedup); err != nil {
		fmt.Fprintf(os.Stderr, "alert-emit InsertAlertDedup: %v\n", err)
		os.Exit(1)
	}
}
