-- D-246 v3 §4.4：fin_llm_call LLM 调用账本（每次真实调用一行，
-- 费用入 D-013 单源账本口径；explorer/feishu_query/deptreport 三消费者覆盖）。
CREATE TABLE IF NOT EXISTS fin_llm_call (
  id                bigserial PRIMARY KEY,
  role              text NOT NULL,          -- 消费面角色（explorer/feishu_query/company_report 等）
  model             text NOT NULL DEFAULT '',
  prompt_tokens     int NOT NULL DEFAULT 0, -- 上游无 usage 事件=0 诚实不虚报
  completion_tokens int NOT NULL DEFAULT 0,
  usd_est           double precision NOT NULL DEFAULT 0, -- 估算成本（费率未配=0）
  elapsed_ms        int NOT NULL DEFAULT 0,
  ok                boolean NOT NULL,
  err               text NOT NULL DEFAULT '',
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS fin_llm_call_created_idx ON fin_llm_call (created_at);
