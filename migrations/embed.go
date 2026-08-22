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

//go:embed 0006_personal_positions.sql
var PersonalPositionsSQL string

//go:embed 0007_investment_records.sql
var InvestmentRecordsSQL string

//go:embed 0008_personal_ledger.sql
var PersonalLedgerSQL string

//go:embed 0009_opportunities_canonical.sql
var OpportunitiesCanonicalSQL string

//go:embed 0010_exchange_configs.sql
var ExchangeConfigsSQL string

//go:embed 0011_exchange_simulated.sql
var ExchangeSimulatedSQL string

//go:embed 0012_canonical_usdt.sql
var CanonicalUsdtSQL string

//go:embed 0032_perp_funding_type.sql
var PerpFundingTypeSQL string

//go:embed 0033_autotrade.sql
var AutotradeSQL string

//go:embed 0034_autotrade_state.sql
var AutotradeStateSQL string

//go:embed 0035_stats_anchor.sql
var StatsAnchorSQL string

//go:embed 0036_combo_identity.sql
var ComboIdentitySQL string
