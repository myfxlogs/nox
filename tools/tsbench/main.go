// nox TimescaleDB 评估 · 三查询基准 harness（评估票 10-19）。
// 直接调 nox 真实 pgstore.Store 三方法（FactsSince/LatestFacts/FactsWindowStats），
// 端到端含 pgx 连接池/预编译/Scan 开销 = 生产实况口径。零 nox 树改动。
// 用法: bench <dsn> <标签>
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"nox/internal/store/pgstore"
)

type bench struct {
	name string
	run  func(ctx context.Context, s *pgstore.Store) (int, error)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: bench <dsn> <label>")
		os.Exit(2)
	}
	dsn, label := os.Args[1], os.Args[2]
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgstore.New: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	warm := 3
	runs := 10
	benches := []bench{
		{"FactsSince_funding_7d", func(ctx context.Context, s *pgstore.Store) (int, error) {
			fs, err := s.FactsSince(ctx, "funding", "binance", "BTCUSDT", time.Now().Add(-7*24*time.Hour))
			return len(fs), err
		}},
		{"FactsSince_position_90d", func(ctx context.Context, s *pgstore.Store) (int, error) {
			fs, err := s.FactsSince(ctx, "position", "binance", "BTCUSDT", time.Now().Add(-90*24*time.Hour))
			return len(fs), err
		}},
		{"LatestFacts_all_empty", func(ctx context.Context, s *pgstore.Store) (int, error) {
			fs, err := s.LatestFacts(ctx, "", "", "")
			return len(fs), err
		}},
		{"LatestFacts_pointed", func(ctx context.Context, s *pgstore.Store) (int, error) {
			fs, err := s.LatestFacts(ctx, "funding", "binance", "BTCUSDT")
			return len(fs), err
		}},
		{"FactsWindowStats_funding_24h", func(ctx context.Context, s *pgstore.Store) (int, error) {
			m, err := s.FactsWindowStats(ctx, "funding", "binance", "BTCUSDT", 24*time.Hour)
			return boolInt(m != 0), err
		}},
		{"FactsWindowStats_optparity_7d", func(ctx context.Context, s *pgstore.Store) (int, error) {
			m, err := s.FactsWindowStats(ctx, "opt_parity_dev", "binance", "BTCUSDT", 7*24*time.Hour)
			return boolInt(m != 0), err
		}},
	}

	for _, b := range benches {
		// warmup
		for i := 0; i < warm; i++ {
			if _, err := b.run(ctx, s); err != nil {
				fmt.Fprintf(os.Stderr, "%s warmup: %v\n", b.name, err)
				os.Exit(1)
			}
		}
		var durs []time.Duration
		rows := 0
		for i := 0; i < runs; i++ {
			t0 := time.Now()
			n, err := b.run(ctx, s)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s run: %v\n", b.name, err)
				os.Exit(1)
			}
			durs = append(durs, time.Since(t0))
			rows = n
		}
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		med := durs[len(durs)/2]
		fmt.Printf("%s\t%s\tmedian=%s\tmin=%s\tmax=%s\trows=%d\n", label, b.name, med, durs[0], durs[len(durs)-1], rows)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
