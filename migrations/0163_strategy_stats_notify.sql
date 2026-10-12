-- 0163 D-004 P1#3：strategy_stats.disabled 变更 → NOTIFY，替代 60s 轮询。
-- 覆盖进程内外一切写者（淘汰引擎进程内自写 / cmd/resettle / psql 手改），
-- 进程内 bus.Topic 够不到跨进程写者，故走 DB 级触发器。
CREATE OR REPLACE FUNCTION strategy_stats_notify() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_notify('strategy_stats', NEW.strategy); RETURN NEW; END $$;
DROP TRIGGER IF EXISTS strategy_stats_notify ON strategy_stats;
CREATE TRIGGER strategy_stats_notify AFTER INSERT OR UPDATE OF disabled ON strategy_stats
    FOR EACH ROW EXECUTE FUNCTION strategy_stats_notify();
