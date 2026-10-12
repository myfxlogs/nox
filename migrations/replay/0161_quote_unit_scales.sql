-- 0161 D-594：报价单位归一层。broker_symbol_map 加 price_scale（原生价×系数=canonical
-- USD/lb 口径价）与 qty_scale（contract_size 原生单位→canonical 单位数/手）两列。
-- NULL=未标定：疑似商品类（同 canonical 存在非 1 标定行）未标定行 → canonical 发射抑制
-- + 名义额 fail-closed；非敏感品种 NULL 按 1.0 计。
-- 回填：既有行全置 1.0 定基线 → 铜行按生产实采值定标（价带实证见施工提示词 §1：
-- ~14500=USD/吨、~6.6=USD/磅、~654=¢/磅；39050818 CS=250 由 mt_symbol_meta 亲采）。
-- canonical=USD/lb：$/ton→1/2204.62，¢/lb→0.01，$/lb→1；qty_scale=一手 canonical lb 数。
ALTER TABLE broker_symbol_map ADD COLUMN price_scale double precision;
ALTER TABLE broker_symbol_map ADD COLUMN qty_scale double precision;

UPDATE broker_symbol_map SET price_scale = 1.0, qty_scale = 1.0 WHERE price_scale IS NULL;

-- 铜行定标：生产现存 XCUUSD 映射行（1279967/1350684/3001157/59892915）全为 USD/吨
-- 报价（~14500 带实证）→ price_scale=1/2204.62（$/ton→$/lb），qty_scale=2204.62
-- （CS=1 吨/手 → 2204.62 lb/手）。
-- qty_scale=contract_size 原生单位 → canonical 单位 的换算系数（列值=系数，非单位数/手；
-- 提示词 §2 示例列的 2204.62/55115.6/2000/250 为 CS×qty_scale 所得单位数/手，与
-- 公式 CS_raw×qty_scale×price_canonical 一致）。
-- S0 取证的历史脏数据 venue（277842155/39050818/591959217/61585565）已不在
-- broker_accounts——broker_symbol_map.venue 有 FK，无法补插标定行；其 XCUUSD 脏
-- ticks 由 S5 DELETE 清洗。若此类 venue 日后重入，行将未标定 → 敏感 canonical
-- emitPlan 抑制发射 + 名义额 fail-closed（本设计正是为此场景兜底）。
UPDATE broker_symbol_map SET price_scale = 1.0/2204.62, qty_scale = 2204.62
WHERE canonical = 'XCUUSD' AND venue IN ('1279967', '1350684', '3001157', '59892915');

-- D-594 §S4 别名：MT5 XCUUSD → join key XCU 与 CEX COPPERUSDT → COPPER 收敛同一池。
INSERT INTO fin_symbol_aliases (alias, canonical, note)
SELECT 'XCU', 'COPPER', 'D-594：XCUUSD→XCU 与 COPPERUSDT→COPPER 双侧 join 收敛'
WHERE NOT EXISTS (SELECT 1 FROM fin_symbol_aliases WHERE alias = 'XCU');
