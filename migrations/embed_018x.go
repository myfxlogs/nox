package migrations

import _ "embed"

var (
	//go:embed 0182_binance_earn_seed.sql
	BinanceEarnSeedSQL string // D-130 返修 A 案：binance:earn 凭据复制补种 + binance:funding 行摘除

	//go:embed 0183_drop_binance_earn.sql
	DropBinanceEarnSQL string // D-133：binance earn 面整体摘除（counter-migration 收口 0182 播种）
)

//go:embed 0184_drop_cnfi_repo.sql
var DropCnfiRepoSQL string // D-139：CNFI/逆回购面 13 表幂等 DROP（生产已备份）

//go:embed 0185_facts_kind_ts_idx.sql
var FactsKindTsIdxSQL string // D-152 fixup：facts(kind,ts)——retention 超窗候选范围寻址
