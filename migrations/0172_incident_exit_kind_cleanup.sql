-- 0172_incident_exit_kind_cleanup.sql —— D-032 R1：清误打的事故 exit_kind。
-- 旧 markOpenIncident 对全腿零成交的 FAILED 也打 legged_unwind——该形态是 maker 超时
-- 撤单/全腿被拒的零敞口干净终态（D-018 设计），非事故；误打会触 D-032 事故闸全拒开
-- （#503/#504 实证）。按形态清全部同构行，不写死 id；幂等可重跑。
UPDATE positions p SET exit_kind = NULL
WHERE p.exit_kind = 'legged_unwind'
  AND p.status = 'FAILED'
  AND NOT EXISTS (SELECT 1 FROM position_legs l
                  WHERE l.position_id = p.id AND l.status = 'FILLED');
