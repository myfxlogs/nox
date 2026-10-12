-- D-220/D-225⑤：部门枚举十一部化——fin_employee_registry.dept 旧八标签映射新十一标签。
-- 采集/链上→数据、执行→交易、通知→IR；研究/风控/运维/账本同名保留。
UPDATE fin_employee_registry SET dept = '数据' WHERE dept IN ('采集', '链上');
UPDATE fin_employee_registry SET dept = '交易' WHERE dept = '执行';
UPDATE fin_employee_registry SET dept = 'IR'   WHERE dept = '通知';
