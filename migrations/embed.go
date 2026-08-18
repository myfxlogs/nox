package migrations

import _ "embed"

//go:embed 0001_init.sql
var InitSQL string

//go:embed 0002_broker_accounts.sql
var BrokerAccountsSQL string

//go:embed 0003_broker_accounts_shorten.sql
var BrokerAccountsShortenSQL string
