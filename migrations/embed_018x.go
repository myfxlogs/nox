package migrations

import _ "embed"

var (
	//go:embed 0182_binance_earn_seed.sql
	BinanceEarnSeedSQL string // D-130 返修 A 案：binance:earn 凭据复制补种 + binance:funding 行摘除
)
