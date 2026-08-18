package migrations

import _ "embed"

//go:embed 0001_init.sql
var InitSQL string

//go:embed 0002_broker_accounts.sql
var BrokerAccountsSQL string

//go:embed 0003_broker_accounts_shorten.sql
var BrokerAccountsShortenSQL string

//go:embed 0004_broker_commission.sql
var BrokerCommissionSQL string

//go:embed 0005_broker_symbol_map_contract.sql
var BrokerSymbolMapContractSQL string
