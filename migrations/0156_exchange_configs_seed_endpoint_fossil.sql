-- 0156: exchange_configs 种子端点化石纠偏（9-28 化石清剿批次）
-- 0083/0084 的 INSERT 种子写的是当时为真的代理机裸 IP:8443——该 IP 现已被代理机
-- iptables 对外 DROP（9-27 加固），任何按种子新装的环境会得到不可达端点。
-- 历史迁移不篡改（其 INSERT 记录当时事实）；本迁移用 WHERE 守卫把「仍是死 IP 的行」
-- 纠为官方端点：生产库已是 cex.myfxlogs.org 域名 → WHERE 不命中 → 幂等零行为变化；
-- 新装环境种子行命中 → 纠为部署中立的官方值，再由部署侧经 dashboard/DB 指到本环境代理。
UPDATE exchange_configs SET base_url = 'https://api.binance.com/api'
  WHERE venue = 'binance' AND venue_type = 'spot' AND base_url LIKE 'https://192.229.85.35%';
UPDATE exchange_configs SET base_url = 'https://fapi.binance.com/fapi'
  WHERE venue = 'binance' AND venue_type = 'perp' AND base_url LIKE 'https://192.229.85.35%';
