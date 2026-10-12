// Command migrate —— D-212 M1 baseline 重放验证器：对 DSN 空库执行指定 SQL 文件。
// 迁移器包随 internal/store 在 M2.x 搬入；本命令仅作 0001_baseline.sql 落库可行性检查。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: migrate <sql-file>")
		os.Exit(2)
	}
	dsn := os.Getenv("ARB_PG_DSN")
	if dsn == "" {
		dsn = "postgres://arb:arb@127.0.0.1:5434/arb_test"
	}
	sql, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	var n int
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_tables WHERE schemaname='public'`).Scan(&n); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("tables:", n)
}
