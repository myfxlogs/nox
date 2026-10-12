#!/usr/bin/env bash
# D-496 P-B：迁移 0142 干跑验证（测试/克隆库专用，禁生产）。
# 用法：scripts/d496_migration_dryrun.sh <目标库名> [源库名]
#   源库名缺省=nox（只读 pg_dump 克隆）；目标库先 DROP 再建——务必传专用测试库。
# 产出：迁移前/后行数对拍 + 键域宪法校验（R1 裸 venue / R2 闭集 type / R3 生成列 /
#       R4 账号级 fact symbol=NULL / FK 孤儿 / 死表清除）。
set -euo pipefail
DB="${1:?usage: $0 <scratch_db> [source_db]}"
SRC="${2:-nox}"
PSQL="docker exec nox-postgres psql -U nox -d"

# 行数对拍：某库全表 count(*)
rowcounts() {
  docker exec nox-postgres psql -U nox -d "$1" -Atc \
    "SELECT 'SELECT '''||table_name||''', count(*) FROM '||quote_ident(table_name)||';' \
     FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'" |
  docker exec -i nox-postgres psql -U nox -d "$1" -At | sort -t'|' -k1,1
}

echo "== clone $SRC -> $DB"
$PSQL postgres -c "DROP DATABASE IF EXISTS $DB"
$PSQL postgres -c "CREATE DATABASE $DB"
docker exec nox-postgres bash -c "pg_dump -U nox -d $SRC | psql -U nox -d $DB -q"

rowcounts "$DB" > /tmp/d496_rows_before.txt
echo "== apply 0142"
docker exec -i nox-postgres psql -U nox -d "$DB" -v ON_ERROR_STOP=1 -q -f - \
  < "$(dirname "$0")/../migrations/0142_keydomain_constitution.sql"
docker exec nox-postgres psql -U nox -d "$DB" -q -c \
  "INSERT INTO schema_migrations (version) VALUES (142) ON CONFLICT DO NOTHING" || true
rowcounts "$DB" > /tmp/d496_rows_after.txt

echo "== row-count diff（应只列已 DROP 的死表）"
join -t'|' -j1 /tmp/d496_rows_before.txt \
  <(comm -23 <(cut -d'|' -f1 /tmp/d496_rows_before.txt) \
             <(docker exec nox-postgres psql -U nox -d "$DB" -Atc \
    "SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'" | sort) \
   | sed 's/$/|DROPPED/') 2>/dev/null || true
comm -12 <(cut -d'|' -f1 /tmp/d496_rows_before.txt) <(cut -d'|' -f1 /tmp/d496_rows_after.txt) \
  | while read -r t; do
      b=$(grep -F "$t|" /tmp/d496_rows_before.txt | cut -d'|' -f2)
      a=$(grep -F "$t|" /tmp/d496_rows_after.txt | cut -d'|' -f2)
      [ "$b" = "$a" ] || echo "ROWDIFF $t $b -> $a"
    done

echo "== 宪法校验（全部应=0 或预期值）"
$PSQL "$DB" -Atc "
SELECT 'colon_in_bare_venue',
  (SELECT count(*) FROM facts WHERE venue LIKE '%:%') +
  (SELECT count(*) FROM position_legs WHERE venue LIKE '%:%') +
  (SELECT count(*) FROM ticks WHERE venue LIKE '%:%') +
  (SELECT count(*) FROM broker_accounts WHERE venue LIKE '%:%')
UNION ALL SELECT 'facts_symbol_eq_venue', count(*) FROM facts WHERE symbol = venue
UNION ALL SELECT 'facts_bad_venue_type', count(*) FROM facts
  WHERE venue_type NOT IN ('mt4','mt5','perp','spot','funding','earn','option','unified','')
UNION ALL SELECT 'bsm_fk_orphans', count(*) FROM broker_symbol_map m
  WHERE NOT EXISTS (SELECT 1 FROM broker_accounts b WHERE b.venue = m.venue)
UNION ALL SELECT 'facts_account_null_sym(期望~9万)', count(*) FROM facts WHERE symbol IS NULL
UNION ALL SELECT 'dead_tables_left', count(*) FROM information_schema.tables
  WHERE table_name IN ('rules','symbol_map','trigger_states','venue_accounts',
                       '_d087_repair_backup_20260823','facts_okx_fh_backup_20260917')"
