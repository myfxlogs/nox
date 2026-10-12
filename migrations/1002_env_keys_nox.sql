-- D-230 改名原子票：app_config/app_secrets 键名 ARB_→NOX_（历史 replay 迁移播种 ARB_ 键，
-- 本迁移统一改名使与代码读径一致；已改名库幂等——WHERE 过滤后零行）。
-- 冲突裁决：NOX_ 同名行已存在时删 ARB_ 旧行（新键优先），再统一改剩余名。
DELETE FROM app_config a USING app_config b
  WHERE a."key" LIKE 'ARB\_%' ESCAPE '\' AND b."key" = 'NOX_' || substring(a."key" from 5);
UPDATE app_config  SET "key" = 'NOX_' || substring("key" from 5) WHERE "key" LIKE 'ARB\_%' ESCAPE '\';
DELETE FROM app_secrets a USING app_secrets b
  WHERE a."key" LIKE 'ARB\_%' ESCAPE '\' AND b."key" = 'NOX_' || substring(a."key" from 5);
UPDATE app_secrets SET "key" = 'NOX_' || substring("key" from 5) WHERE "key" LIKE 'ARB\_%' ESCAPE '\';
COMMENT ON TABLE public.app_secrets IS 'D-515：密钥类配置密文通道（AES-256-GCM nonce‖ct，key=env NOX_MASTER_KEY；禁写日志禁回显）';
COMMENT ON COLUMN public.xasset_allow_combos.notional_usd IS 'D-367：组合级名义额（USD）；NULL=回退 NOX_OPP_NOTIONAL_USD 全局缺省';
