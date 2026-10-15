-- 1031_paper_qty_param.sql —— D-545：纸面名义额参数化（D-532 遗留断链裁定）。
-- 裁定逻辑：paper qty 按真金意图额度级取 $5,000（本金池 $11,317 的 ~4.4%/
-- 单仓——与 DCI/earn 实单量级同族）；venue 最小名义额（binance $5/okx
-- lotSz 计）三约束取 max 的频宽余量充足。如实注记：费率线性模型下
-- accrued:fee 比与 qty 无关——本参数裁定的是「纸面=真金代表额」语义，
-- 经济比修复靠信号强度/持有期（graduation 判据如实不放宽，D-468 不动）。
INSERT INTO app_config (key, value, note) VALUES
('paper_qty_usd', '5000', 'D-545 纸面仓位名义额（$——虚拟仓复价单位；改值须新 D#）')
ON CONFLICT (key) DO NOTHING;
