-- 0142_keydomain_constitution.sql — D-496 键域宪法：全库 venue 键域归一（一步到位）
-- R1 venue=裸端点（禁含':'）；R2 venue_type=闭集通道型；R3 venue_key=GENERATED ONLY；
-- R4 symbol=原生品种名 NULL=非品种域；R5 通道注册处=broker_accounts∪exchange_configs。
-- 回填实拍依据（2026-09-26 生产库）：
--   position_legs.venue 1296 行全复合；facts 'deribit:option' 1,002,834 行、symbol=venue 90,228 行；
--   income_events.account_id∈{18,25}=exchange_configs.id；paper_spot_carry.venue='a×b' 配对串；
--   autotrade_logs 73 行 venue 空串（归位 ''/''）；knowledge_entries.venue 含逗号列表 1 行。
-- venue_type '' = 非通道域（cmc 数据源/历史空值），CHECK 允许 ''。

-- ============ 死物清除（S-§2.3：表均零行且代码零 SQL 引用，已逐表核实）============
ALTER TABLE alerts DROP COLUMN IF EXISTS rule_id;
DROP TABLE IF EXISTS trigger_states;
DROP TABLE IF EXISTS rules;
DROP TABLE IF EXISTS venue_accounts;
DROP TABLE IF EXISTS symbol_map;
DROP TABLE IF EXISTS _d087_repair_backup_20260823;
DROP TABLE IF EXISTS facts_okx_fh_backup_20260917;

-- ============ 注册表（broker_accounts 的 platform→venue_type 转换置于文件尾部——
-- facts/knowledge_entries 回填需引用 platform 列反解 MT 类型，须先消费后删除）============

-- exchange_configs：venue_type 收进 R2 枚举 + venue 裸域 CHECK。
ALTER TABLE exchange_configs ADD CONSTRAINT exchange_configs_venue_bare_chk CHECK (venue NOT LIKE '%:%');
ALTER TABLE exchange_configs ADD CONSTRAINT exchange_configs_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot','funding','earn','option','unified'));
ALTER TABLE exchange_configs ADD COLUMN venue_key text
    GENERATED ALWAYS AS (venue || ':' || venue_type) STORED;
COMMENT ON COLUMN exchange_configs.venue_key IS 'R3 生成投影：venue:venue_type，禁手写';

-- ============ position_legs ============
ALTER TABLE position_legs ADD COLUMN venue_type text;
UPDATE position_legs SET venue_type = split_part(venue, ':', 2) WHERE position(':' IN venue) > 0;
UPDATE position_legs SET venue = split_part(venue, ':', 1);
ALTER TABLE position_legs ALTER COLUMN venue_type SET NOT NULL;
ALTER TABLE position_legs ALTER COLUMN venue_type SET DEFAULT '';
ALTER TABLE position_legs ADD COLUMN venue_key text
    GENERATED ALWAYS AS (CASE WHEN venue_type = '' THEN venue ELSE venue || ':' || venue_type END) STORED;
DROP INDEX position_legs_client_order_id_idx;
ALTER TABLE position_legs DROP COLUMN account;
CREATE UNIQUE INDEX position_legs_client_order_id_idx ON position_legs (venue, client_order_id)
    WHERE client_order_id <> '';
ALTER TABLE position_legs ADD CONSTRAINT position_legs_venue_bare_chk CHECK (venue NOT LIKE '%:%');
ALTER TABLE position_legs ADD CONSTRAINT position_legs_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));
COMMENT ON COLUMN position_legs.venue IS 'R1 裸端点';
COMMENT ON COLUMN position_legs.venue_type IS 'R2 通道型（''=非通道历史空值）';
COMMENT ON COLUMN position_legs.venue_key IS 'R3 生成投影，业务侧复合键读此列';

-- position_cashflows.venue 同为腿级复合域（118 行全复合），与 legs 同法归一。
ALTER TABLE position_cashflows ADD COLUMN venue_type text;
UPDATE position_cashflows SET venue_type = split_part(venue, ':', 2) WHERE position(':' IN venue) > 0;
UPDATE position_cashflows SET venue = split_part(venue, ':', 1);
ALTER TABLE position_cashflows ALTER COLUMN venue_type SET NOT NULL;
ALTER TABLE position_cashflows ALTER COLUMN venue_type SET DEFAULT '';
ALTER TABLE position_cashflows ADD COLUMN venue_key text
    GENERATED ALWAYS AS (CASE WHEN venue_type = '' THEN venue ELSE venue || ':' || venue_type END) STORED;
ALTER TABLE position_cashflows ADD CONSTRAINT position_cashflows_venue_bare_chk CHECK (venue NOT LIKE '%:%');
ALTER TABLE position_cashflows ADD CONSTRAINT position_cashflows_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));

-- ============ opportunities / opportunities_tick / autotrade_logs / xasset_allow_combos ============
-- venue_buy/venue_sell 复合 → (venue_*, venue_type_*) 存列 + venue_key_* 生成投影。
ALTER TABLE opportunities ADD COLUMN venue_type_buy text, ADD COLUMN venue_type_sell text;
UPDATE opportunities SET venue_type_buy = split_part(venue_buy, ':', 2) WHERE position(':' IN venue_buy) > 0;
UPDATE opportunities SET venue_type_sell = split_part(venue_sell, ':', 2) WHERE position(':' IN venue_sell) > 0;
UPDATE opportunities SET venue_buy = split_part(venue_buy, ':', 1), venue_sell = split_part(venue_sell, ':', 1);
UPDATE opportunities SET venue_type_buy = '' WHERE venue_type_buy IS NULL;
UPDATE opportunities SET venue_type_sell = '' WHERE venue_type_sell IS NULL;
ALTER TABLE opportunities ALTER COLUMN venue_type_buy SET NOT NULL, ALTER COLUMN venue_type_buy SET DEFAULT '';
ALTER TABLE opportunities ALTER COLUMN venue_type_sell SET NOT NULL, ALTER COLUMN venue_type_sell SET DEFAULT '';
ALTER TABLE opportunities ADD COLUMN venue_key_buy text
    GENERATED ALWAYS AS (CASE WHEN venue_type_buy = '' THEN venue_buy ELSE venue_buy || ':' || venue_type_buy END) STORED;
ALTER TABLE opportunities ADD COLUMN venue_key_sell text
    GENERATED ALWAYS AS (CASE WHEN venue_type_sell = '' THEN venue_sell ELSE venue_sell || ':' || venue_type_sell END) STORED;
ALTER TABLE opportunities ADD CONSTRAINT opp_venue_bare_chk
    CHECK (venue_buy NOT LIKE '%:%' AND venue_sell NOT LIKE '%:%');
ALTER TABLE opportunities ADD CONSTRAINT opp_venue_type_chk
    CHECK (venue_type_buy IN ('mt4','mt5','perp','spot','funding','earn','option','unified','')
       AND venue_type_sell IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));

ALTER TABLE opportunities_tick ADD COLUMN venue_type_buy text, ADD COLUMN venue_type_sell text;
UPDATE opportunities_tick SET venue_type_buy = split_part(venue_buy, ':', 2) WHERE position(':' IN venue_buy) > 0;
UPDATE opportunities_tick SET venue_type_sell = split_part(venue_sell, ':', 2) WHERE position(':' IN venue_sell) > 0;
UPDATE opportunities_tick SET venue_buy = split_part(venue_buy, ':', 1), venue_sell = split_part(venue_sell, ':', 1);
UPDATE opportunities_tick SET venue_type_buy = '' WHERE venue_type_buy IS NULL;
UPDATE opportunities_tick SET venue_type_sell = '' WHERE venue_type_sell IS NULL;
ALTER TABLE opportunities_tick ALTER COLUMN venue_type_buy SET NOT NULL, ALTER COLUMN venue_type_buy SET DEFAULT '';
ALTER TABLE opportunities_tick ALTER COLUMN venue_type_sell SET NOT NULL, ALTER COLUMN venue_type_sell SET DEFAULT '';
ALTER TABLE opportunities_tick ADD COLUMN venue_key_buy text
    GENERATED ALWAYS AS (CASE WHEN venue_type_buy = '' THEN venue_buy ELSE venue_buy || ':' || venue_type_buy END) STORED;
ALTER TABLE opportunities_tick ADD COLUMN venue_key_sell text
    GENERATED ALWAYS AS (CASE WHEN venue_type_sell = '' THEN venue_sell ELSE venue_sell || ':' || venue_type_sell END) STORED;
ALTER TABLE opportunities_tick ADD CONSTRAINT opptick_venue_bare_chk
    CHECK (venue_buy NOT LIKE '%:%' AND venue_sell NOT LIKE '%:%');
ALTER TABLE opportunities_tick ADD CONSTRAINT opptick_venue_type_chk
    CHECK (venue_type_buy IN ('mt4','mt5','perp','spot','funding','earn','option','unified','')
       AND venue_type_sell IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));

ALTER TABLE autotrade_logs ADD COLUMN venue_type_buy text, ADD COLUMN venue_type_sell text;
UPDATE autotrade_logs SET venue_type_buy = split_part(venue_buy, ':', 2) WHERE position(':' IN venue_buy) > 0;
UPDATE autotrade_logs SET venue_type_sell = split_part(venue_sell, ':', 2) WHERE position(':' IN venue_sell) > 0;
UPDATE autotrade_logs SET venue_buy = split_part(venue_buy, ':', 1), venue_sell = split_part(venue_sell, ':', 1);
UPDATE autotrade_logs SET venue_type_buy = '' WHERE venue_type_buy IS NULL;
UPDATE autotrade_logs SET venue_type_sell = '' WHERE venue_type_sell IS NULL;
ALTER TABLE autotrade_logs ALTER COLUMN venue_type_buy SET NOT NULL, ALTER COLUMN venue_type_buy SET DEFAULT '';
ALTER TABLE autotrade_logs ALTER COLUMN venue_type_sell SET NOT NULL, ALTER COLUMN venue_type_sell SET DEFAULT '';
ALTER TABLE autotrade_logs ADD COLUMN venue_key_buy text
    GENERATED ALWAYS AS (CASE WHEN venue_type_buy = '' THEN venue_buy ELSE venue_buy || ':' || venue_type_buy END) STORED;
ALTER TABLE autotrade_logs ADD COLUMN venue_key_sell text
    GENERATED ALWAYS AS (CASE WHEN venue_type_sell = '' THEN venue_sell ELSE venue_sell || ':' || venue_type_sell END) STORED;
ALTER TABLE autotrade_logs ADD CONSTRAINT autotrade_logs_venue_bare_chk
    CHECK (venue_buy NOT LIKE '%:%' AND venue_sell NOT LIKE '%:%');
ALTER TABLE autotrade_logs ADD CONSTRAINT autotrade_logs_venue_type_chk
    CHECK (venue_type_buy IN ('mt4','mt5','perp','spot','funding','earn','option','unified','')
       AND venue_type_sell IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));

ALTER TABLE xasset_allow_combos ADD COLUMN venue_type_buy text, ADD COLUMN venue_type_sell text;
UPDATE xasset_allow_combos SET venue_type_buy = split_part(venue_buy, ':', 2) WHERE position(':' IN venue_buy) > 0;
UPDATE xasset_allow_combos SET venue_type_sell = split_part(venue_sell, ':', 2) WHERE position(':' IN venue_sell) > 0;
UPDATE xasset_allow_combos SET venue_buy = split_part(venue_buy, ':', 1), venue_sell = split_part(venue_sell, ':', 1);
UPDATE xasset_allow_combos SET venue_type_buy = '' WHERE venue_type_buy IS NULL;
UPDATE xasset_allow_combos SET venue_type_sell = '' WHERE venue_type_sell IS NULL;
ALTER TABLE xasset_allow_combos ALTER COLUMN venue_type_buy SET NOT NULL, ALTER COLUMN venue_type_buy SET DEFAULT '';
ALTER TABLE xasset_allow_combos ALTER COLUMN venue_type_sell SET NOT NULL, ALTER COLUMN venue_type_sell SET DEFAULT '';
ALTER TABLE xasset_allow_combos ADD COLUMN venue_key_buy text
    GENERATED ALWAYS AS (CASE WHEN venue_type_buy = '' THEN venue_buy ELSE venue_buy || ':' || venue_type_buy END) STORED;
ALTER TABLE xasset_allow_combos ADD COLUMN venue_key_sell text
    GENERATED ALWAYS AS (CASE WHEN venue_type_sell = '' THEN venue_sell ELSE venue_sell || ':' || venue_type_sell END) STORED;
ALTER TABLE xasset_allow_combos DROP CONSTRAINT xasset_allow_combos_canonical_buy_canonical_sell_venue_buy__key;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_uniq UNIQUE
    (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell);
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_venue_bare_chk
    CHECK (venue_buy NOT LIKE '%:%' AND venue_sell NOT LIKE '%:%');
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_venue_type_chk
    CHECK (venue_type_buy IN ('mt4','mt5','perp','spot','funding','earn','option','unified','')
       AND venue_type_sell IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));

-- ============ position_swap_snapshots ============
ALTER TABLE position_swap_snapshots ADD COLUMN venue_type text;
UPDATE position_swap_snapshots SET venue_type = split_part(venue, ':', 2) WHERE position(':' IN venue) > 0;
UPDATE position_swap_snapshots SET venue = split_part(venue, ':', 1);
UPDATE position_swap_snapshots SET venue_type = '' WHERE venue_type IS NULL;
ALTER TABLE position_swap_snapshots ALTER COLUMN venue_type SET NOT NULL;
ALTER TABLE position_swap_snapshots ALTER COLUMN venue_type SET DEFAULT '';
ALTER TABLE position_swap_snapshots ADD COLUMN venue_key text
    GENERATED ALWAYS AS (CASE WHEN venue_type = '' THEN venue ELSE venue || ':' || venue_type END) STORED;
ALTER TABLE position_swap_snapshots ADD CONSTRAINT pss_venue_bare_chk CHECK (venue NOT LIKE '%:%');
ALTER TABLE position_swap_snapshots ADD CONSTRAINT pss_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));

-- ============ carry_graduations：venue(CEX 裸域，carry 恒 perp)+mt_venue(MT 裸域，NULL=非cfd) ============
ALTER TABLE carry_graduations ADD COLUMN venue_type text NOT NULL DEFAULT 'perp';
ALTER TABLE carry_graduations ADD CONSTRAINT cg_venue_bare_chk
    CHECK (venue NOT LIKE '%:%' AND (mt_venue IS NULL OR mt_venue NOT LIKE '%:%'));
ALTER TABLE carry_graduations ADD CONSTRAINT cg_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));

-- ============ ticks（分区表：generated+CHECK PG16 实测兼容）============
ALTER TABLE ticks ADD COLUMN venue_key text
    GENERATED ALWAYS AS (CASE WHEN venue_type = '' THEN venue ELSE venue || ':' || venue_type END) STORED;
ALTER TABLE ticks ADD CONSTRAINT ticks_venue_bare_chk CHECK (venue NOT LIKE '%:%');
-- F4：CHECK 前先归一越界 venue_type（历史/陈旧测试库脏值；生产实拍零行=no-op）——
-- 数字 venue（MT 账号号）→mt5，其余（CEX 行情行）→perp。
UPDATE ticks SET venue_type = CASE WHEN venue ~ '^[0-9]+$' THEN 'mt5' ELSE 'perp' END
    WHERE venue_type NOT IN ('mt4','mt5','perp','spot');
ALTER TABLE ticks ADD CONSTRAINT ticks_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot'));
COMMENT ON COLUMN ticks.venue IS 'R1 裸端点（本表本就合规范式）';
COMMENT ON COLUMN ticks.venue_key IS 'R3 生成投影';

-- ============ facts：deribit:option 拆列 + venue_type 按 kind 映射 + symbol=venue→NULL ============
ALTER TABLE facts ADD COLUMN venue_type text;
UPDATE facts SET venue_type = 'option', venue = 'deribit' WHERE venue = 'deribit:option';
UPDATE facts SET venue_type = CASE
    WHEN venue IN ('binance','okx','gate','bybit','bitget','kraken','coinbase','htx','bingx','mexc','kucoin','bitmart','lbank','bitget','hyperliquid')
         THEN 'perp'   -- CEX fact 全部 perp 域（funding/swap_settled 实拍 kind 闭集）
    WHEN venue = 'cmc' THEN ''                       -- 行情数据源，非交易通道
    WHEN venue ~ '^[0-9]+$' THEN COALESCE(           -- MT 账号号→注册处 platform 反解
        (SELECT CASE WHEN ba.platform = 0 THEN 'mt4' ELSE 'mt5' END
           FROM broker_accounts ba WHERE ba.venue = facts.venue), 'mt5')
    ELSE '' END
WHERE venue_type IS NULL;
ALTER TABLE facts ALTER COLUMN venue_type SET NOT NULL;
ALTER TABLE facts ALTER COLUMN venue_type SET DEFAULT '';
ALTER TABLE facts ALTER COLUMN symbol DROP NOT NULL;
UPDATE facts SET symbol = NULL WHERE symbol = venue;      -- 账号级 fact（init_margin_rate/triple_swap_day）语义归位
ALTER TABLE facts ADD CONSTRAINT facts_venue_bare_chk CHECK (venue NOT LIKE '%:%');
ALTER TABLE facts ADD CONSTRAINT facts_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));
ALTER TABLE facts ADD CONSTRAINT facts_symbol_chk CHECK (symbol IS NULL OR symbol <> '');
ALTER TABLE facts ADD COLUMN venue_key text
    GENERATED ALWAYS AS (CASE WHEN venue_type = '' THEN venue ELSE venue || ':' || venue_type END) STORED;
CREATE INDEX facts_venue_kind_symbol_ts_idx ON facts (venue, kind, symbol, ts);
CREATE INDEX facts_venuekey_kind_symbol_ts_idx ON facts (venue_key, kind, symbol, ts); -- F5：venue_key 查询面索引
COMMENT ON COLUMN facts.symbol IS 'R4 原生品种名；NULL=账号级/非品种域 fact';

-- ============ broker_symbol_map：FK→注册处（1314615 无审计删除案的结构性修复）============
DELETE FROM broker_symbol_map WHERE venue NOT IN (SELECT venue FROM broker_accounts);
ALTER TABLE broker_symbol_map ADD CONSTRAINT broker_symbol_map_venue_fk
    FOREIGN KEY (venue) REFERENCES broker_accounts (venue);
ALTER TABLE broker_symbol_map ADD CONSTRAINT bsm_venue_bare_chk CHECK (venue NOT LIKE '%:%');

-- ============ income_events：account_id→exchange_config_id（FK 回注册处）+(venue,venue_type) 反解 ============
-- R5：账户身份归 exchange_configs 外键（同 venue 多账户域下游标/唯一键按 config 行区分，
-- 不归 (venue,venue_type)——venue_type 仅为 R2 通道型标注）。
ALTER TABLE income_events ADD COLUMN venue_type text;
UPDATE income_events ie SET venue_type = ec.venue_type
    FROM exchange_configs ec WHERE ie.account_id = ec.id;
UPDATE income_events SET venue_type = '' WHERE venue_type IS NULL;
ALTER TABLE income_events ALTER COLUMN venue_type SET NOT NULL;
ALTER TABLE income_events ALTER COLUMN venue_type SET DEFAULT '';
ALTER TABLE income_events DROP CONSTRAINT income_events_tran_uniq;
ALTER TABLE income_events RENAME COLUMN account_id TO exchange_config_id;
ALTER TABLE income_events ADD CONSTRAINT income_events_config_fk
    FOREIGN KEY (exchange_config_id) REFERENCES exchange_configs (id);
ALTER TABLE income_events ADD CONSTRAINT income_events_tran_uniq UNIQUE (exchange_config_id, tran_id);
ALTER TABLE income_events ADD CONSTRAINT income_events_venue_bare_chk CHECK (venue NOT LIKE '%:%');
ALTER TABLE income_events ADD CONSTRAINT income_events_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''));
ALTER TABLE income_events ADD COLUMN venue_key text
    GENERATED ALWAYS AS (CASE WHEN venue_type = '' THEN venue ELSE venue || ':' || venue_type END) STORED;

-- ============ knowledge_entries.venue → venue_keys text[]（元素=venue_key 域）============
ALTER TABLE knowledge_entries ADD COLUMN venue_keys text[] NOT NULL DEFAULT '{}';
UPDATE knowledge_entries
SET venue_keys = ARRAY(
    SELECT btrim(x) FROM unnest(string_to_array(venue, ',')) AS x WHERE btrim(x) <> ''
) WHERE venue <> '';
-- 裸账号号元素补 ':mt4|mt5' 尾（注册处 platform 反解，缺→mt5）
UPDATE knowledge_entries SET venue_keys = (
    SELECT array_agg(CASE
        WHEN e LIKE '%:%' THEN e
        WHEN e ~ '^[0-9]+$' THEN e || ':' || COALESCE(
            (SELECT CASE WHEN ba.platform = 0 THEN 'mt4' ELSE 'mt5' END
               FROM broker_accounts ba WHERE ba.venue = e), 'mt5')
        ELSE e END)
    FROM unnest(venue_keys) AS e
) WHERE venue <> '';
ALTER TABLE knowledge_entries DROP COLUMN venue;
COMMENT ON COLUMN knowledge_entries.venue_keys IS 'venue_key 域数组（元素 venue:venue_type 或 CEX 裸名特例）';

-- ============ paper_spot_carry.venue 'a×b' → venue_a/venue_b 裸域两列 ============
-- venue_b='' 缺省（非 NULL——保 (venue_a,venue_b,symbol) 部分唯一索引可推断、ON CONFLICT 可用）。
ALTER TABLE paper_spot_carry ADD COLUMN venue_a text, ADD COLUMN venue_b text NOT NULL DEFAULT '';
UPDATE paper_spot_carry SET venue_a = split_part(venue, '×', 1),
                            venue_b = split_part(venue, '×', 2);
UPDATE paper_spot_carry SET venue_b = '' WHERE venue_b = venue_a OR venue_b IS NULL; -- 单腿串 'okx'
ALTER TABLE paper_spot_carry ALTER COLUMN venue_a SET NOT NULL;
ALTER TABLE paper_spot_carry DROP COLUMN venue;  -- 级联删旧 (venue,symbol) 部分唯一索引
CREATE UNIQUE INDEX paper_spot_carry_active_uniq ON paper_spot_carry (venue_a, venue_b, symbol)
    WHERE status IN ('active','demo');
ALTER TABLE paper_spot_carry ADD CONSTRAINT psc_venue_bare_chk
    CHECK (venue_a NOT LIKE '%:%' AND venue_b NOT LIKE '%:%'
       AND (mt_venue IS NULL OR mt_venue NOT LIKE '%:%'));
COMMENT ON COLUMN paper_spot_carry.venue_a IS 'R1 裸端点 A 腿（MT=账号号，CEX=交易所名）';
COMMENT ON COLUMN paper_spot_carry.venue_b IS 'R1 裸端点 B 腿；''=历史单腿行';

-- ============ broker_accounts（尾部执行：platform 已被 facts/ke 回填消费完毕）============
ALTER TABLE broker_accounts ADD COLUMN venue_type text;
UPDATE broker_accounts SET venue_type = CASE platform WHEN 0 THEN 'mt4' ELSE 'mt5' END;
ALTER TABLE broker_accounts ALTER COLUMN venue_type SET NOT NULL;
ALTER TABLE broker_accounts DROP COLUMN platform;
ALTER TABLE broker_accounts DROP COLUMN user_id;
ALTER TABLE broker_accounts ADD CONSTRAINT broker_accounts_venue_bare_chk CHECK (venue NOT LIKE '%:%');
ALTER TABLE broker_accounts ADD CONSTRAINT broker_accounts_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5'));
COMMENT ON COLUMN broker_accounts.venue IS 'R1 裸端点：MT 账号号';
COMMENT ON COLUMN broker_accounts.venue_type IS 'R2 通道型：mt4|mt5（原 platform int 魔法值 0/1）';

-- ============ watchlist_symbols.venues：值已是 venue_key 域，契约声明 ============
COMMENT ON COLUMN watchlist_symbols.venues IS 'venue_key 域数组（元素 venue:venue_type），R3 契约声明';

-- ============ schema 自说明条款（SSOT §4 末条）：全部键域列域归属 COMMENT ============
COMMENT ON COLUMN exchange_configs.venue IS 'R1 裸端点：CEX 交易所名';
COMMENT ON COLUMN exchange_configs.venue_type IS 'R2 通道型：perp|spot|funding|earn|unified';
COMMENT ON COLUMN position_legs.symbol IS 'R4 原生品种名（venue 侧原生形）';
COMMENT ON COLUMN position_cashflows.venue IS 'R1 裸端点';
COMMENT ON COLUMN position_cashflows.venue_type IS 'R2 通道型';
COMMENT ON COLUMN position_cashflows.venue_key IS 'R3 生成投影';
COMMENT ON COLUMN opportunities.venue_buy IS 'R1 裸端点（买腿）';
COMMENT ON COLUMN opportunities.venue_sell IS 'R1 裸端点（卖腿）';
COMMENT ON COLUMN opportunities.venue_type_buy IS 'R2 通道型（买腿）';
COMMENT ON COLUMN opportunities.venue_type_sell IS 'R2 通道型（卖腿）';
COMMENT ON COLUMN opportunities.venue_key_buy IS 'R3 生成投影（买腿）';
COMMENT ON COLUMN opportunities.venue_key_sell IS 'R3 生成投影（卖腿）';
COMMENT ON COLUMN opportunities_tick.venue_buy IS 'R1 裸端点（买腿）';
COMMENT ON COLUMN opportunities_tick.venue_sell IS 'R1 裸端点（卖腿）';
COMMENT ON COLUMN opportunities_tick.venue_type_buy IS 'R2 通道型（买腿）';
COMMENT ON COLUMN opportunities_tick.venue_type_sell IS 'R2 通道型（卖腿）';
COMMENT ON COLUMN opportunities_tick.venue_key_buy IS 'R3 生成投影（买腿）';
COMMENT ON COLUMN opportunities_tick.venue_key_sell IS 'R3 生成投影（卖腿）';
COMMENT ON COLUMN autotrade_logs.venue_buy IS 'R1 裸端点（买腿）';
COMMENT ON COLUMN autotrade_logs.venue_sell IS 'R1 裸端点（卖腿）';
COMMENT ON COLUMN autotrade_logs.venue_type_buy IS 'R2 通道型（买腿）';
COMMENT ON COLUMN autotrade_logs.venue_type_sell IS 'R2 通道型（卖腿）';
COMMENT ON COLUMN autotrade_logs.venue_key_buy IS 'R3 生成投影（买腿）';
COMMENT ON COLUMN autotrade_logs.venue_key_sell IS 'R3 生成投影（卖腿）';
COMMENT ON COLUMN xasset_allow_combos.venue_buy IS 'R1 裸端点（买腿）';
COMMENT ON COLUMN xasset_allow_combos.venue_sell IS 'R1 裸端点（卖腿）';
COMMENT ON COLUMN xasset_allow_combos.venue_type_buy IS 'R2 通道型（买腿）';
COMMENT ON COLUMN xasset_allow_combos.venue_type_sell IS 'R2 通道型（卖腿）';
COMMENT ON COLUMN xasset_allow_combos.venue_key_buy IS 'R3 生成投影（买腿）';
COMMENT ON COLUMN xasset_allow_combos.venue_key_sell IS 'R3 生成投影（卖腿）';
COMMENT ON COLUMN position_swap_snapshots.venue IS 'R1 裸端点';
COMMENT ON COLUMN position_swap_snapshots.venue_type IS 'R2 通道型';
COMMENT ON COLUMN position_swap_snapshots.venue_key IS 'R3 生成投影';
COMMENT ON COLUMN carry_graduations.venue IS 'R1 裸端点（CEX 交易所名）';
COMMENT ON COLUMN carry_graduations.mt_venue IS 'R1 裸端点（MT 账号号；NULL=非 MT 腿组合）';
COMMENT ON COLUMN carry_graduations.venue_type IS 'R2 通道型';
COMMENT ON COLUMN ticks.venue_type IS 'R2 通道型';
COMMENT ON COLUMN facts.venue IS 'R1 裸端点';
COMMENT ON COLUMN facts.venue_type IS 'R2 通道型（''=非通道如 cmc）';
COMMENT ON COLUMN facts.venue_key IS 'R3 生成投影';
COMMENT ON COLUMN income_events.exchange_config_id IS 'R5 账户身份外键→exchange_configs.id（同 venue 多账户域下游标/唯一键按配置行区分）';
COMMENT ON COLUMN income_events.venue IS 'R1 裸端点（CEX 交易所名）';
COMMENT ON COLUMN income_events.venue_type IS 'R2 通道型';
COMMENT ON COLUMN income_events.venue_key IS 'R3 生成投影';
COMMENT ON COLUMN income_events.symbol IS 'R4 原生品种名';
