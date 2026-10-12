-- 1005_dept_reports.sql —— D-244：部门日报→经理摘要落库面。
-- dept='manager' 行=公司日报（经理摘要）；其余=十一部部报+账本专项（dept='账本' 行即财务日报，
-- 同表同键零额外表）。PK(dept,report_date) 幂等——当日重跑覆盖最新正文。

CREATE TABLE IF NOT EXISTS fin_dept_reports (
    dept              TEXT        NOT NULL,
    report_date       DATE        NOT NULL,
    body              TEXT        NOT NULL,
    elapsed_ms        INTEGER     NOT NULL DEFAULT 0,
    prompt_tokens     INTEGER     NOT NULL DEFAULT 0,
    completion_tokens INTEGER     NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (dept, report_date)
);
