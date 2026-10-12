-- D-190：员工花名册（daemon 编制显性化；心跳=现有 loop 节拍，零新调度器）。
CREATE TABLE IF NOT EXISTS fin_employee_registry (
    name         text PRIMARY KEY,            -- safego 协程名（main:xxx / wire_*:xxx）
    role         text NOT NULL DEFAULT '',    -- 职能（采集/执行/风控/推送…）
    dept         text NOT NULL DEFAULT '',    -- 部门域
    assembly     text NOT NULL DEFAULT '',    -- 装配点（wire 文件）
    cadence_sec  int  NOT NULL DEFAULT 0,     -- 期望节拍秒（0=常驻型，仅以 running 判生死）
    running      bool NOT NULL DEFAULT false, -- fn 在跑（GoLoop 启动置真、退出置假）
    exits        int  NOT NULL DEFAULT 0,     -- 退出累计（退出史）
    task         text NOT NULL DEFAULT '',    -- 当前任务摘要
    beat_ts      timestamptz,                 -- 最近心跳
    enabled      bool NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
