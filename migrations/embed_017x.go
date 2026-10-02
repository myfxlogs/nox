package migrations

import _ "embed"

var (
	//go:embed 0170_broker_swap_free.sql
	BrokerSwapFreeSQL string // D-014 S1：broker_accounts.swap_free 旗标列 + 3001157 置 true + exchange_configs.taker_fee_bps

	//go:embed 0171_maker_fee_enable.sql
	MakerFeeEnableSQL string // D-018 后段：exchange_configs.maker_fee_bps + app_config maker 启用键
)
