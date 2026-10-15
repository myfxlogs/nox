package main

// explorer_daily_test.go —— 探矿日扫提案浮面锚：daily+new 计数+canonical top3；
// probe/weekly 与非 new 行不计；N=0 照常印行。

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExplorerDaily(t *testing.T) {
	dsn := os.Getenv("NOX_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://nox:nox@127.0.0.1:5434/digest_dead_test"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("pg: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS explorer_proposals (scan_kind text, status text, canonical text)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	pool.Exec(ctx, `DELETE FROM explorer_proposals WHERE canonical LIKE 'xedq%'`)
	defer pool.Exec(ctx, `DELETE FROM explorer_proposals WHERE canonical LIKE 'xedq%'`)
	if _, err := pool.Exec(ctx, `INSERT INTO explorer_proposals (scan_kind, status, canonical) VALUES
		('daily','new','xedq_a'),('daily','new','xedq_a'),('daily','new','xedq_b'),
		('probe','new','xedq_c'),('daily','expired','xedq_d'),('weekly','new','xedq_e')`); err != nil {
		t.Fatal(err)
	}
	n, tops, err := loadExplorerDaily(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 || len(tops) == 0 || tops[0] != "xedq_a×2" {
		t.Fatalf("n=%d tops=%v", n, tops)
	}
	if out := renderExplorerDaily(n, tops); !strings.Contains(out, "探矿日扫提案") || !strings.Contains(out, "xedq_a×2") {
		t.Fatalf("render: %s", out)
	}
	if out := renderExplorerDaily(0, nil); !strings.Contains(out, "探矿日扫提案：0") {
		t.Fatalf("N=0 照常印行: %s", out)
	}
}
