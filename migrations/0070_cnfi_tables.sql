-- 0070_cnfi_tables.sql - CN-FI 境内人民币固收块（D-226/D-229/D-230/D-235；SSOT=docs/cnfi/CN-FI-P0.5规格包-2026-09-05-ZCode.md v0.2）
-- 全部表无个人数据种子（D-047）；tenor 空串=单点序列（C1 规范化键，禁 nullable 歧义）。

CREATE TABLE cnfi_series (
    as_of       date NOT NULL,                 -- 中国市场业务日
    series      text NOT NULL,                 -- cgb / ig_aaa / bank_aaa（P1.5 扩：ig_aa/ncd_aaa/t2cd_aaam）
    tenor       text NOT NULL DEFAULT '',      -- 单点序列=''（全角分号注记：曲线仅允许 3m/1y/2y/3y/5y/10y/30y2y 主源暂不可达，预留）。切分器在注释内遇 ASCII 分号会切断语句——D-234 P2 现世实证）
    value_bp    numeric NOT NULL,              -- 统一 bp（源百分比 ×100）
    source      text NOT NULL,                 -- chinabond / manual:*
    source_ref  text NOT NULL,                 -- run_id 或 'manual:<来源>:<证据指针>'（R2：禁 NULL 隐藏血缘）
    fetched_at  timestamptz NOT NULL,          -- UTC
    CONSTRAINT cnfi_series_tenor_check CHECK (tenor IN ('', '3m', '1y', '2y', '3y', '5y', '10y', '30y')),
    CONSTRAINT cnfi_series_uniq UNIQUE (as_of, series, tenor)
);
CREATE INDEX cnfi_series_lookup_idx ON cnfi_series (series, tenor, as_of DESC);

CREATE TABLE cnfi_calendar (
    cal_date        date PRIMARY KEY,
    is_business_day boolean NOT NULL,
    cal_version     text NOT NULL,             -- 种子/更新递增；缺失=calendar_unknown（F4：不断流结论）
    note            text
);

CREATE TABLE cnfi_instruments (
    code      text PRIMARY KEY,
    kind      text NOT NULL,                   -- fund / savings_bond / ncd
    name      text NOT NULL,
    is_active boolean NOT NULL DEFAULT true,
    meta      jsonb
);

CREATE TABLE cnfi_nav (
    code       text NOT NULL REFERENCES cnfi_instruments (code),
    nav_date   date NOT NULL,
    nav        numeric NOT NULL,
    source     text NOT NULL,
    fetched_at timestamptz NOT NULL,
    CONSTRAINT cnfi_nav_uniq UNIQUE (code, nav_date)
);

CREATE TABLE cnfi_signal_config (
    key          text PRIMARY KEY,
    value        jsonb NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   text NOT NULL,
    evidence_ref text                           -- 研究纪要指针（F8 变更史=YAGNI，纪要即审计轨迹）
);

CREATE TABLE cnfi_probe_runs (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_type      text NOT NULL,               -- daily / backfill
    started_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    status        text NOT NULL DEFAULT 'running', -- running / success / partial / failed
    max_as_of     date,
    rows_written  integer NOT NULL DEFAULT 0,
    rows_changed  integer NOT NULL DEFAULT 0,
    error_summary text
);

CREATE TABLE cnfi_signal_state (
    key            text PRIMARY KEY,
    observed_state text NOT NULL DEFAULT 'observe', -- observe / armed / fired（observe-only 期一律 observe，C11）
    last_value     numeric,
    sample_n       integer,
    observed_as_of date,
    last_alert_at  timestamptz,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    evidence_ref   text
);
