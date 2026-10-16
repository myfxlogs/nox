-- SWAPARB 配对等价键表（D-558）：canonical → 产品类直挂。
-- 病灶（复审席 SG 10-16 审计钉死）：broker_symbol_map.product_group=broker 本地 MT5
-- CalcMode 目录分类，同品跨所异组（SG 实拍 XAUUSD=110660304:cfd_leverage /
-- 15032313:fx / 3001157:cfd_spot 三归法）→ sameProductGroup 跨所配对全灭。
-- 110660304 把 BTCUSDT（无歧义现货 crypto）也归 cfd_leverage——组值=broker 房规目录，
-- 非产品身份。等价键改锚 canonical 产品类：本表=产品身份裁定登记册
-- （无行=未裁定 → 配对 fail-closed 拒+日志留痕促归类；类异=真异品拒，
-- 470 防护语义保留）。新 canonical 走本表 INSERT 追加非改码
-- （D-554 fin_position_strategy_map 同范式）。
-- 种子=SG 生产 canonical 全集 111 行（broker_symbol_map DISTINCT canonical，
-- 2026-10-16 实拍）：fx=36 / crypto=52 / index=12 / energy=2 / metal=9。
CREATE TABLE IF NOT EXISTS fin_canonical_product_class (
  canonical     TEXT PRIMARY KEY,
  product_class TEXT NOT NULL CHECK (product_class IN ('fx','crypto','index','energy','metal'))
);
COMMENT ON TABLE fin_canonical_product_class IS 'D-558 SWAPARB 配对等价键：canonical→产品类裁定登记册（无行=未裁定 fail-closed 拒）';
COMMENT ON COLUMN fin_canonical_product_class.canonical IS '键域=broker_symbol_map.canonical 同域（产品身份键，非 broker 本地目录分类）';
COMMENT ON COLUMN fin_canonical_product_class.product_class IS '产品类闭集 fx/crypto/index/energy/metal——等价键，同类才可配对（真异品拒）';
INSERT INTO fin_canonical_product_class (canonical, product_class) VALUES
  -- fx（36）
  ('AUDCAD','fx'),('AUDCHF','fx'),('AUDJPY','fx'),('AUDNZD','fx'),('AUDSGD','fx'),('AUDUSD','fx'),
  ('CADCHF','fx'),('CADJPY','fx'),('CHFJPY','fx'),('CHFSGD','fx'),
  ('EURAUD','fx'),('EURCAD','fx'),('EURCHF','fx'),('EURGBP','fx'),('EURJPY','fx'),('EURNZD','fx'),('EURSGD','fx'),('EURUSD','fx'),
  ('GBPAUD','fx'),('GBPCAD','fx'),('GBPCHF','fx'),('GBPJPY','fx'),('GBPNZD','fx'),('GBPSGD','fx'),('GBPUSD','fx'),
  ('NOKSEK','fx'),('NZDCAD','fx'),('NZDCHF','fx'),('NZDJPY','fx'),('NZDSGD','fx'),('NZDUSD','fx'),('SGDJPY','fx'),
  ('USDCAD','fx'),('USDCHF','fx'),('USDJPY','fx'),('USDSGD','fx'),
  -- crypto（52）
  ('1INCHUSDT','crypto'),('AAVEUSDT','crypto'),('ADAUSDT','crypto'),('ALGOUSDT','crypto'),('APEUSDT','crypto'),
  ('APTUSDT','crypto'),('ARBUSDT','crypto'),('ATOMUSDT','crypto'),('AVAXUSDT','crypto'),('AXSUSDT','crypto'),
  ('BATUSDT','crypto'),('BCHUSDT','crypto'),('BNBUSDT','crypto'),('BTGUSDT','crypto'),('BTCUSDT','crypto'),
  ('CHZUSDT','crypto'),('COMPUSDT','crypto'),('CRVUSDT','crypto'),('DASHUSDT','crypto'),('DOGEUSDT','crypto'),
  ('DOTUSDT','crypto'),('ENJUSDT','crypto'),('EOSUSDT','crypto'),('ETCUSDT','crypto'),('ETHUSDT','crypto'),
  ('FETUSDT','crypto'),('FILUSDT','crypto'),('FLOWUSDT','crypto'),('GRTUSDT','crypto'),('ICPUSDT','crypto'),
  ('IMXUSDT','crypto'),('LDOUSDT','crypto'),('LINKUSDT','crypto'),('LRCUSDT','crypto'),('LTCUSDT','crypto'),
  ('MANAUSDT','crypto'),('MATICUSDT','crypto'),('NEARUSDT','crypto'),('OPUSDT','crypto'),('SANDUSDT','crypto'),
  ('SNXUSDT','crypto'),('SOLUSDT','crypto'),('STXUSDT','crypto'),('SUSHIUSDT','crypto'),('TRXUSDT','crypto'),
  ('UMAUSDT','crypto'),('UNIUSDT','crypto'),('XLMUSDT','crypto'),('XRPUSDT','crypto'),('XTZUSDT','crypto'),
  ('ZECUSDT','crypto'),('ZRXUSDT','crypto'),
  -- index（12）
  ('AUS200','index'),('ES35','index'),('FR40','index'),('GER40','index'),('HK50','index'),('JP225','index'),
  ('NAS100','index'),('SPX500','index'),('STOXX50','index'),('UK100','index'),('US2000','index'),('US30','index'),
  -- energy（2）
  ('BRENT','energy'),('WTI','energy'),
  -- metal（9）
  ('SILVER','metal'),('XALUSD','metal'),('XAUUSD','metal'),('XCUUSD','metal'),('XNIUSD','metal'),
  ('XPBUSD','metal'),('XPDUSD','metal'),('XPTUSD','metal'),('XZNUSD','metal')
ON CONFLICT (canonical) DO NOTHING;
