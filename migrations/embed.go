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

//go:embed 0037_verification_stats.sql
var VerificationStatsSQL string

//go:embed 0038_settlement_typed_columns.sql
var SettlementTypedColumnsSQL string

//go:embed 0039_watchlist.sql
var WatchlistSQL string

//go:embed 0040_watchlist_venues.sql
var WatchlistVenuesSQL string

//go:embed 0041_analyst_ro_role.sql
var AnalystRoRoleSQL string

//go:embed 0042_ai_settings.sql
var AiSettingsSQL string

//go:embed 0043_position_swap_snapshots.sql
var PositionSwapSnapshotsSQL string

//go:embed 0044_broker_symbol_map_source.sql
var BrokerSymbolMapSourceSQL string

//go:embed 0045_broker_accounts_is_real.sql
var BrokerAccountsIsRealSQL string

//go:embed 0046_account_website.sql
var AccountWebsiteSQL string

//go:embed 0047_arb_ro_revoke_credentials.sql
var ArbRoRevokeCredentialsSQL string

//go:embed 0048_position_intent_fields.sql
var PositionIntentFieldsSQL string

//go:embed 0049_cashflows_leg_no.sql
var CashflowsLegNoSQL string

//go:embed 0050_broker_symbol_map_product_group.sql
var BrokerSymbolMapProductGroupSQL string

//go:embed 0051_delete_precious_metals_cross_currency.sql
var DeletePreciousMetalsCrossCurrencySQL string

//go:embed 0052_position_notional_usd.sql
var PositionNotionalUsdSQL string

//go:embed 0053_position_closing_started_at.sql
var PositionClosingStartedAtSQL string

//go:embed 0054_cashflows_d193.sql
var CashflowsD193SQL string

//go:embed 0055_settlements_d193.sql
var SettlementsD193SQL string

//go:embed 0056_capital_accounts.sql
var CapitalAccountsSQL string

//go:embed 0057_capital_snapshots.sql
var CapitalSnapshotsSQL string

//go:embed 0058_ledger_d193.sql
var LedgerD193SQL string

//go:embed 0059_hypotheses.sql
var HypothesesSQL string

//go:embed 0060_capital_decisions.sql
var CapitalDecisionsSQL string

//go:embed 0061_position_reviews.sql
var PositionReviewsSQL string

//go:embed 0062_execution_controls.sql
var ExecutionControlsSQL string

//go:embed 0063_position_legs_attempt.sql
var PositionLegsAttemptSQL string

//go:embed 0064_live_gate.sql
var LiveGateSQL string

//go:embed 0065_cmc_tables.sql
var CmcTablesSQL string

//go:embed 0066_personal_cmc_link.sql
var PersonalCMCLinkSQL string

//go:embed 0067_cmc_positions_meta.sql
var CMCPositionsMetaSQL string

//go:embed 0068_trx_stake_tables.sql
var TrxStakeTablesSQL string

//go:embed 0069_cmc_ws_event_driven.sql
var CmcWsEventDrivenSQL string

//go:embed 0071_trx_vote_apr.sql
var TrxVoteAprSQL string
