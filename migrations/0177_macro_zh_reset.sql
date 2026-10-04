-- 0176 D-053 ①：MQL5 端点 en→zh 切换的存量清洗——
-- 旧 en 抓取行（name=英文）与 zh 新行（name=中文）经唯一键 (source,event_time_utc,currency,name)
-- 并存 → 日报双语重复；zh 上线首拉前清空 mql5 源行（2026-10-04 对拍钉死，后续只落 zh 行）。
DELETE FROM macro_event WHERE source = 'mql5';
