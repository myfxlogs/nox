--
-- PostgreSQL database dump
--


-- Dumped from database version 16.15 (Debian 16.15-1.pgdg13+2)
-- Dumped by pg_dump version 16.15 (Debian 16.15-1.pgdg13+2)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: pg_stat_statements; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public;


--
-- Name: EXTENSION pg_stat_statements; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_stat_statements IS 'track planning and execution statistics of all SQL statements executed';


--
-- Name: strategy_stats_notify(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.strategy_stats_notify() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN PERFORM pg_notify('strategy_stats', NEW.strategy); RETURN NEW; END $$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: ai_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ai_settings (
    id integer DEFAULT 1 NOT NULL,
    base_url text,
    model text,
    api_key text,
    enabled boolean DEFAULT true NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ai_settings_single_row CHECK ((id = 1))
);


--
-- Name: alerts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.alerts (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    level text NOT NULL,
    message text NOT NULL,
    acked boolean DEFAULT false NOT NULL,
    position_id bigint,
    CONSTRAINT alerts_level_check CHECK ((level = ANY (ARRAY['info'::text, 'warn'::text, 'critical'::text])))
);


--
-- Name: alerts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.alerts ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.alerts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: app_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.app_config (
    key text NOT NULL,
    value text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    note text
);


--
-- Name: app_secrets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.app_secrets (
    key text NOT NULL,
    value_enc bytea NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE app_secrets; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.app_secrets IS 'D-515：密钥类配置密文通道（AES-256-GCM nonce‖ct，key=env ARB_MASTER_KEY；禁写日志禁回显）';


--
-- Name: COLUMN app_secrets.key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.app_secrets.key IS 'env 键名（FEISHU_*/AI_*/LONGBRIDGE_* 等密钥类）';


--
-- Name: COLUMN app_secrets.value_enc; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.app_secrets.value_enc IS 'AES-256-GCM 密文（nonce 前缀拼接）';


--
-- Name: COLUMN app_secrets.note; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.app_secrets.note IS '运维注记';


--
-- Name: asset_equiv; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.asset_equiv (
    id bigint NOT NULL,
    asset_class text NOT NULL,
    canonical text NOT NULL,
    venue_type text NOT NULL,
    venue text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: asset_equiv_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.asset_equiv_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: asset_equiv_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.asset_equiv_id_seq OWNED BY public.asset_equiv.id;


--
-- Name: autotrade_allow_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.autotrade_allow_rules (
    id bigint NOT NULL,
    kind text NOT NULL,
    value text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    simulated boolean DEFAULT false NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT autotrade_allow_rules_kind_check CHECK ((kind = ANY (ARRAY['venue'::text, 'canonical'::text])))
);


--
-- Name: autotrade_allow_rules_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.autotrade_allow_rules_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: autotrade_allow_rules_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.autotrade_allow_rules_id_seq OWNED BY public.autotrade_allow_rules.id;


--
-- Name: autotrade_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.autotrade_logs (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    opportunity_id text NOT NULL,
    position_id bigint,
    strategy text NOT NULL,
    canonical text NOT NULL,
    venue_buy text NOT NULL,
    venue_sell text NOT NULL,
    qty double precision NOT NULL,
    status text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    net_bps double precision DEFAULT 0 NOT NULL,
    venue_type_buy text DEFAULT ''::text NOT NULL,
    venue_type_sell text DEFAULT ''::text NOT NULL,
    venue_key_buy text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_buy = ''::text) THEN venue_buy
    ELSE ((venue_buy || ':'::text) || venue_type_buy)
END) STORED,
    venue_key_sell text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_sell = ''::text) THEN venue_sell
    ELSE ((venue_sell || ':'::text) || venue_type_sell)
END) STORED,
    CONSTRAINT autotrade_logs_venue_bare_chk CHECK (((venue_buy !~~ '%:%'::text) AND (venue_sell !~~ '%:%'::text))),
    CONSTRAINT autotrade_logs_venue_type_chk CHECK (((venue_type_buy = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])) AND (venue_type_sell = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text]))))
);


--
-- Name: COLUMN autotrade_logs.venue_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.autotrade_logs.venue_buy IS 'R1 裸端点（买腿）';


--
-- Name: COLUMN autotrade_logs.venue_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.autotrade_logs.venue_sell IS 'R1 裸端点（卖腿）';


--
-- Name: COLUMN autotrade_logs.venue_type_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.autotrade_logs.venue_type_buy IS 'R2 通道型（买腿）';


--
-- Name: COLUMN autotrade_logs.venue_type_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.autotrade_logs.venue_type_sell IS 'R2 通道型（卖腿）';


--
-- Name: COLUMN autotrade_logs.venue_key_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.autotrade_logs.venue_key_buy IS 'R3 生成投影（买腿）';


--
-- Name: COLUMN autotrade_logs.venue_key_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.autotrade_logs.venue_key_sell IS 'R3 生成投影（卖腿）';


--
-- Name: autotrade_logs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.autotrade_logs ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.autotrade_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: autotrade_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.autotrade_state (
    state text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    halted_on date,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: broker_accounts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.broker_accounts (
    id bigint NOT NULL,
    venue text NOT NULL,
    host text NOT NULL,
    port integer NOT NULL,
    password text NOT NULL,
    server text DEFAULT ''::text NOT NULL,
    alias text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'connected'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    commission_bps double precision,
    is_real boolean,
    website text DEFAULT ''::text NOT NULL,
    venue_type text NOT NULL,
    swap_free boolean DEFAULT false NOT NULL,
    CONSTRAINT broker_accounts_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT broker_accounts_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text])))
);


--
-- Name: COLUMN broker_accounts.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.broker_accounts.venue IS 'R1 裸端点：MT 账号号';


--
-- Name: COLUMN broker_accounts.venue_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.broker_accounts.venue_type IS 'R2 通道型：mt4|mt5（原 platform int 魔法值 0/1）';


--
-- Name: broker_accounts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.broker_accounts ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.broker_accounts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: broker_symbol_map; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.broker_symbol_map (
    id bigint NOT NULL,
    venue text NOT NULL,
    broker_symbol text NOT NULL,
    canonical text NOT NULL,
    contract_size double precision,
    source text DEFAULT 'manual'::text NOT NULL,
    product_group text,
    is_primary boolean DEFAULT false NOT NULL,
    price_scale double precision,
    qty_scale double precision,
    CONSTRAINT bsm_venue_bare_chk CHECK ((venue !~~ '%:%'::text))
);


--
-- Name: broker_symbol_map_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.broker_symbol_map ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.broker_symbol_map_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: carry_graduations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.carry_graduations (
    id bigint NOT NULL,
    paper_id bigint NOT NULL,
    symbol text NOT NULL,
    structure text NOT NULL,
    mt_venue text,
    age_days numeric NOT NULL,
    accrued_usd numeric NOT NULL,
    last_funding_ann numeric,
    status text DEFAULT 'pending'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    decided_at timestamp with time zone,
    decided_note text DEFAULT ''::text NOT NULL,
    combo_id bigint,
    net_all_in_usd numeric,
    flag_warn text,
    venue_a text NOT NULL,
    venue_b text NOT NULL,
    venue text GENERATED ALWAYS AS (
CASE
    WHEN (venue_b = ''::text) THEN venue_a
    ELSE ((venue_a || '×'::text) || venue_b)
END) STORED,
    venue_type_a text DEFAULT ''::text NOT NULL,
    venue_type_b text DEFAULT ''::text NOT NULL,
    venue_key_a text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_a = ''::text) THEN venue_a
    ELSE ((venue_a || ':'::text) || venue_type_a)
END) STORED,
    venue_key_b text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_b = ''::text) THEN venue_b
    ELSE ((venue_b || ':'::text) || venue_type_b)
END) STORED,
    CONSTRAINT carry_graduations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text]))),
    CONSTRAINT cg_venue_a_bare_chk CHECK (((venue_a <> ''::text) AND (venue_a !~~ '%:%'::text) AND (venue_a !~~ '%×%'::text))),
    CONSTRAINT cg_venue_b_bare_chk CHECK (((venue_b !~~ '%:%'::text) AND (venue_b !~~ '%×%'::text))),
    CONSTRAINT cg_venue_type_ab_chk CHECK (((venue_type_a = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text])) AND (venue_type_b = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text]))))
);


--
-- Name: COLUMN carry_graduations.mt_venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.carry_graduations.mt_venue IS 'R1 裸端点（MT 账号号；NULL=非 MT 腿组合）';


--
-- Name: COLUMN carry_graduations.venue_a; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.carry_graduations.venue_a IS 'D-498 配对源列·A 臂裸域（cfd=MT 账号号；spot_carry=CEX 名）';


--
-- Name: COLUMN carry_graduations.venue_b; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.carry_graduations.venue_b IS 'D-498 配对源列·B 臂裸域（cfd=CEX 名；单 venue 行=空串）';


--
-- Name: COLUMN carry_graduations.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.carry_graduations.venue IS 'D-498 展示性配对投影（generated：a 或 a×b，禁写）';


--
-- Name: COLUMN carry_graduations.venue_type_a; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.carry_graduations.venue_type_a IS 'D-555 A 臂 venue_type（源 paper 行透传；存量行=空）';


--
-- Name: COLUMN carry_graduations.venue_type_b; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.carry_graduations.venue_type_b IS 'D-555 B 臂 venue_type（源 paper 行透传；存量行=空）';


--
-- Name: COLUMN carry_graduations.venue_key_a; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.carry_graduations.venue_key_a IS 'D-555 A 臂键域生成列（D-496 口径）';


--
-- Name: COLUMN carry_graduations.venue_key_b; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.carry_graduations.venue_key_b IS 'D-555 B 臂键域生成列（D-496 口径）';


--
-- Name: carry_graduations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.carry_graduations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: carry_graduations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.carry_graduations_id_seq OWNED BY public.carry_graduations.id;


--
-- Name: cex_private_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cex_private_events (
    id bigint NOT NULL,
    venue text NOT NULL,
    venue_type text NOT NULL,
    channel text NOT NULL,
    event_key text DEFAULT ''::text NOT NULL,
    payload jsonb NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: cex_private_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.cex_private_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: cex_private_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.cex_private_events_id_seq OWNED BY public.cex_private_events.id;


--
-- Name: combo_daily_rollup; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.combo_daily_rollup (
    combo_key text NOT NULL,
    day date NOT NULL,
    type text NOT NULL,
    samples integer DEFAULT 0 NOT NULL,
    net_avg numeric(20,8) DEFAULT 0 NOT NULL,
    net_min numeric(20,8) DEFAULT 0 NOT NULL,
    net_max numeric(20,8) DEFAULT 0 NOT NULL,
    annual_avg numeric(10,4),
    first_at timestamp with time zone DEFAULT now() NOT NULL,
    last_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: exchange_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.exchange_configs (
    id bigint NOT NULL,
    venue text NOT NULL,
    venue_type text DEFAULT 'spot'::text NOT NULL,
    label text DEFAULT ''::text NOT NULL,
    api_key text NOT NULL,
    api_secret text NOT NULL,
    passphrase text DEFAULT ''::text NOT NULL,
    base_url text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    simulated boolean DEFAULT false NOT NULL,
    website text DEFAULT ''::text NOT NULL,
    venue_key text GENERATED ALWAYS AS (((venue || ':'::text) || venue_type)) STORED,
    api_key_enc bytea,
    api_secret_enc bytea,
    passphrase_enc bytea,
    taker_fee_bps double precision,
    maker_fee_bps double precision,
    CONSTRAINT exchange_configs_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT exchange_configs_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text])))
);


--
-- Name: COLUMN exchange_configs.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.exchange_configs.venue IS 'R1 裸端点：CEX 交易所名';


--
-- Name: COLUMN exchange_configs.venue_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.exchange_configs.venue_type IS 'R2 通道型：perp|spot|funding|earn|unified|futures';


--
-- Name: COLUMN exchange_configs.venue_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.exchange_configs.venue_key IS 'R3 生成投影：venue:venue_type，禁手写';


--
-- Name: COLUMN exchange_configs.api_key_enc; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.exchange_configs.api_key_enc IS 'D-515：AES-256-GCM 密文（明文 api_key 清空后唯一读源；NULL=未自封）';


--
-- Name: COLUMN exchange_configs.api_secret_enc; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.exchange_configs.api_secret_enc IS 'D-515：AES-256-GCM 密文';


--
-- Name: COLUMN exchange_configs.passphrase_enc; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.exchange_configs.passphrase_enc IS 'D-515：AES-256-GCM 密文';


--
-- Name: exchange_configs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.exchange_configs ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.exchange_configs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: execution_control_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.execution_control_events (
    id bigint NOT NULL,
    scope_type text NOT NULL,
    scope_key text DEFAULT ''::text NOT NULL,
    from_mode text DEFAULT ''::text NOT NULL,
    to_mode text NOT NULL,
    actor text DEFAULT ''::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    evidence_ref text DEFAULT ''::text NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    event_key text NOT NULL,
    CONSTRAINT execution_control_events_scope_type_check CHECK ((scope_type = ANY (ARRAY['global'::text, 'account'::text, 'broker'::text])))
);


--
-- Name: COLUMN execution_control_events.scope_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.execution_control_events.scope_type IS '事件域：global=总闸；account=账户级；broker=CFD 账号管理操作审计（D-493）';


--
-- Name: execution_control_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.execution_control_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.execution_control_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: execution_controls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.execution_controls (
    control_id bigint NOT NULL,
    scope_type text NOT NULL,
    scope_key text DEFAULT ''::text NOT NULL,
    mode text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    approved_by text DEFAULT ''::text NOT NULL,
    confirmed_at timestamp with time zone,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    reason text DEFAULT ''::text NOT NULL,
    config_digest text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT execution_controls_mode_check CHECK ((mode = ANY (ARRAY['READ_ONLY'::text, 'PAPER'::text, 'TEST'::text]))),
    CONSTRAINT execution_controls_scope_type_check CHECK ((scope_type = ANY (ARRAY['global'::text, 'account'::text])))
);


--
-- Name: execution_controls_control_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.execution_controls ALTER COLUMN control_id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.execution_controls_control_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: explorer_proposals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.explorer_proposals (
    id bigint NOT NULL,
    scan_date date NOT NULL,
    proposed_at timestamp with time zone DEFAULT now() NOT NULL,
    scan_kind text NOT NULL,
    source text DEFAULT 'explorer'::text NOT NULL,
    theme text DEFAULT ''::text NOT NULL,
    canonical text NOT NULL,
    best_pair text DEFAULT ''::text NOT NULL,
    direction text DEFAULT ''::text NOT NULL,
    rationale text DEFAULT ''::text NOT NULL,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'new'::text NOT NULL,
    gate_note text DEFAULT ''::text NOT NULL,
    settled_ref text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    probe jsonb,
    decision_ref text,
    probe_position_id bigint,
    CONSTRAINT explorer_proposals_scan_kind_check CHECK ((scan_kind = ANY (ARRAY['daily'::text, 'weekly'::text, 'probe'::text]))),
    CONSTRAINT explorer_proposals_status_check CHECK ((status = ANY (ARRAY['new'::text, 'submitted'::text, 'admitted'::text, 'denied'::text, 'expired'::text, 'proposed'::text, 'approved'::text, 'running'::text, 'closed'::text, 'rejected'::text])))
);


--
-- Name: TABLE explorer_proposals; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.explorer_proposals IS 'D-513 探矿员提案记分板（壳期提案止于收件箱，status 席位手动回填）';


--
-- Name: COLUMN explorer_proposals.scan_date; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.scan_date IS 'BJ 历日（Go 侧算好写入，F1：禁 proposed_at::date 依赖 DB 时区）';


--
-- Name: COLUMN explorer_proposals.scan_kind; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.scan_kind IS '扫描类别：daily=日扫五项/weekly=周深挖';


--
-- Name: COLUMN explorer_proposals.source; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.source IS '提案来源（默认 explorer；B8 统一面并入时免反推）';


--
-- Name: COLUMN explorer_proposals.theme; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.theme IS '周深挖主题（日扫空串）';


--
-- Name: COLUMN explorer_proposals.canonical; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.canonical IS '规范符号键（落库前校验命中已知集）';


--
-- Name: COLUMN explorer_proposals.best_pair; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.best_pair IS '最优腿对 venue_a×venue_b 键形（空=无配对）';


--
-- Name: COLUMN explorer_proposals.direction; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.direction IS '方向（≤16字，LLM 输出截断校验）';


--
-- Name: COLUMN explorer_proposals.rationale; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.rationale IS '一行理由（LLM 叙事）';


--
-- Name: COLUMN explorer_proposals.evidence; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.evidence IS '佐证数字 JSONB（键=输入段字段名）';


--
-- Name: COLUMN explorer_proposals.status; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.status IS '流转态：new/submitted/admitted/denied/expired（手动回填）';


--
-- Name: COLUMN explorer_proposals.gate_note; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.gate_note IS '闸结果注记（席位回填）';


--
-- Name: COLUMN explorer_proposals.settled_ref; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.settled_ref IS '结算引用（席位回填）';


--
-- Name: COLUMN explorer_proposals.probe; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.probe IS 'D-048 探针契约 JSONB（scan_kind=probe 时必填；缺任一字段校验层拒入库）';


--
-- Name: COLUMN explorer_proposals.decision_ref; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.decision_ref IS 'D-048 批准引用 D#（ApproveProbe 时写入）';


--
-- Name: COLUMN explorer_proposals.probe_position_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_proposals.probe_position_id IS 'D-048 批准开单后关联的 PROBE 仓 id';


--
-- Name: explorer_proposals_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.explorer_proposals ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.explorer_proposals_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: explorer_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.explorer_state (
    key text NOT NULL,
    val jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE explorer_state; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.explorer_state IS 'D-513 探矿员跨进程状态（昨快照/diff 幂等键）';


--
-- Name: COLUMN explorer_state.key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_state.key IS '状态键（如 watchlist_snapshot/symbol_map_count）';


--
-- Name: COLUMN explorer_state.val; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.explorer_state.val IS '状态值 JSONB';


--
-- Name: facts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.facts (
    id bigint NOT NULL,
    kind text NOT NULL,
    venue text NOT NULL,
    symbol text,
    value double precision NOT NULL,
    unit text DEFAULT ''::text NOT NULL,
    ts timestamp with time zone NOT NULL,
    src text DEFAULT ''::text NOT NULL,
    venue_type text DEFAULT ''::text NOT NULL,
    venue_key text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type = ''::text) THEN venue
    ELSE ((venue || ':'::text) || venue_type)
END) STORED,
    CONSTRAINT facts_symbol_chk CHECK (((symbol IS NULL) OR (symbol <> ''::text))),
    CONSTRAINT facts_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT facts_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])))
);


--
-- Name: COLUMN facts.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.facts.venue IS 'R1 裸端点';


--
-- Name: COLUMN facts.symbol; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.facts.symbol IS 'R4 原生品种名；NULL=账号级/非品种域 fact';


--
-- Name: COLUMN facts.venue_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.facts.venue_type IS 'R2 通道型（''=非通道如 cmc）';


--
-- Name: COLUMN facts.venue_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.facts.venue_key IS 'R3 生成投影';


--
-- Name: facts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.facts ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.facts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: feishu_push_target; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.feishu_push_target (
    id integer DEFAULT 1 NOT NULL,
    chat_id text DEFAULT ''::text NOT NULL,
    open_id text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT feishu_push_target_single_row CHECK ((id = 1))
);


--
-- Name: fin_canonical_exposure_caps; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_canonical_exposure_caps (
    canonical text NOT NULL,
    cap_usd numeric NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT fin_canonical_exposure_caps_cap_usd_check CHECK ((cap_usd >= (0)::numeric))
);


--
-- Name: fin_capability_constraint; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_capability_constraint (
    id bigint NOT NULL,
    scope text NOT NULL,
    position_id bigint,
    subject text NOT NULL,
    qualifier text DEFAULT ''::text NOT NULL,
    value_num double precision,
    note text DEFAULT ''::text NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT fin_capability_constraint_scope_check CHECK ((scope = ANY (ARRAY['position'::text, 'bucket'::text, 'venue'::text, 'global'::text])))
);


--
-- Name: fin_capability_constraint_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_capability_constraint_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_capability_constraint_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_capability_constraint_id_seq OWNED BY public.fin_capability_constraint.id;


--
-- Name: fin_capability_permission; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_capability_permission (
    id bigint NOT NULL,
    venue text NOT NULL,
    permission text NOT NULL,
    state text NOT NULL,
    probed_at timestamp with time zone DEFAULT now() NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    CONSTRAINT fin_capability_permission_state_check CHECK ((state = ANY (ARRAY['present'::text, 'absent'::text, 'unknown'::text])))
);


--
-- Name: fin_capability_permission_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_capability_permission_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_capability_permission_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_capability_permission_id_seq OWNED BY public.fin_capability_permission.id;


--
-- Name: fin_carry_universe; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_carry_universe (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    canonical text NOT NULL,
    mt_venue text NOT NULL,
    cex_venue text NOT NULL,
    net_annual_pct numeric NOT NULL,
    funding_annual_pct numeric NOT NULL,
    swap_long_pct numeric,
    swap_short_pct numeric,
    swap_free boolean DEFAULT false NOT NULL,
    structure text NOT NULL,
    note text DEFAULT ''::text NOT NULL
);


--
-- Name: fin_carry_universe_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_carry_universe_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_carry_universe_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_carry_universe_id_seq OWNED BY public.fin_carry_universe.id;


--
-- Name: fin_cex_dci_quote; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_cex_dci_quote (
    id bigint NOT NULL,
    venue text NOT NULL,
    underlying text NOT NULL,
    invest_ccy text NOT NULL,
    strike double precision,
    opt_type text,
    delivery_ts timestamp with time zone,
    tenor_days integer,
    apy double precision NOT NULL,
    product_ref text,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    order_ref text DEFAULT ''::text NOT NULL,
    min_amount double precision,
    max_amount double precision,
    purchase_decimal integer
);


--
-- Name: fin_cex_dci_quote_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_cex_dci_quote_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_cex_dci_quote_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_cex_dci_quote_id_seq OWNED BY public.fin_cex_dci_quote.id;


--
-- Name: fin_cex_depth; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_cex_depth (
    id bigint NOT NULL,
    venue text NOT NULL,
    symbol text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    bid1_usd double precision NOT NULL,
    ask1_usd double precision NOT NULL,
    bid1_qty double precision NOT NULL,
    ask1_qty double precision NOT NULL,
    mid_depth_usd double precision NOT NULL
);


--
-- Name: fin_cex_depth_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_cex_depth_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_cex_depth_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_cex_depth_id_seq OWNED BY public.fin_cex_depth.id;


--
-- Name: fin_cex_earn_apr; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_cex_earn_apr (
    id bigint NOT NULL,
    venue character varying(16) NOT NULL,
    ccy character varying(16) NOT NULL,
    product_type character varying(16) NOT NULL,
    apr numeric(10,6) NOT NULL,
    tier_apr text,
    ts timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_cex_earn_apr_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_cex_earn_apr_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_cex_earn_apr_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_cex_earn_apr_id_seq OWNED BY public.fin_cex_earn_apr.id;


--
-- Name: fin_cex_earn_judgment; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_cex_earn_judgment (
    id bigint NOT NULL,
    venue character varying(16) NOT NULL,
    ccy character varying(16) NOT NULL,
    product_type character varying(32) DEFAULT ''::character varying NOT NULL,
    apr numeric(10,6) NOT NULL,
    category character varying(16) NOT NULL,
    verdict character varying(20) NOT NULL,
    reasons text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_cex_earn_judgment_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.fin_cex_earn_judgment ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.fin_cex_earn_judgment_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: fin_cex_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_cex_events (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    venue text NOT NULL,
    symbol text NOT NULL,
    event_type text NOT NULL,
    detail text DEFAULT ''::text NOT NULL
);


--
-- Name: fin_cex_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_cex_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_cex_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_cex_events_id_seq OWNED BY public.fin_cex_events.id;


--
-- Name: fin_cex_radar; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_cex_radar (
    ts timestamp with time zone DEFAULT now() NOT NULL,
    venue text NOT NULL,
    symbol text NOT NULL,
    funding_ann numeric,
    vol24h numeric,
    mark numeric,
    idx numeric,
    hedge_canonical text DEFAULT ''::text NOT NULL,
    tier text DEFAULT 'T3'::text NOT NULL
);


--
-- Name: fin_cex_spread; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_cex_spread (
    id bigint NOT NULL,
    symbol character varying(32) NOT NULL,
    venue_buy character varying(16) NOT NULL,
    venue_sell character varying(16) NOT NULL,
    price_buy numeric(18,8) NOT NULL,
    price_sell numeric(18,8) NOT NULL,
    spread_bps numeric(10,4) NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_cex_spread_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_cex_spread_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_cex_spread_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_cex_spread_id_seq OWNED BY public.fin_cex_spread.id;


--
-- Name: fin_cex_universe; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_cex_universe (
    venue text NOT NULL,
    symbol text NOT NULL,
    base text NOT NULL,
    underlying_class text DEFAULT ''::text NOT NULL,
    status text NOT NULL,
    launch_ts timestamp with time zone,
    first_seen timestamp with time zone DEFAULT now() NOT NULL,
    last_seen timestamp with time zone DEFAULT now() NOT NULL,
    hedge_canonical text DEFAULT ''::text NOT NULL,
    tier text DEFAULT 'T3'::text NOT NULL,
    last_funding_ann numeric,
    last_vol24h numeric,
    last_alert_ts timestamp with time zone,
    ct_val numeric,
    last_basis_bps numeric
);


--
-- Name: fin_condition_gap; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_condition_gap (
    id bigint NOT NULL,
    template_id text DEFAULT ''::text NOT NULL,
    kind text NOT NULL,
    subject text NOT NULL,
    qualifier text DEFAULT ''::text NOT NULL,
    request_text text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    requested_at timestamp with time zone,
    resolved_at timestamp with time zone,
    resolve_note text,
    CONSTRAINT fin_condition_gap_status_check CHECK ((status = ANY (ARRAY['open'::text, 'requested'::text, 'fulfilled'::text, 'waived'::text])))
);


--
-- Name: fin_condition_gap_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_condition_gap_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_condition_gap_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_condition_gap_id_seq OWNED BY public.fin_condition_gap.id;


--
-- Name: fin_dci_order; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_dci_order (
    id bigint NOT NULL,
    request_id text NOT NULL,
    venue text NOT NULL,
    template_id text DEFAULT ''::text NOT NULL,
    product_ref text DEFAULT ''::text NOT NULL,
    underlying text NOT NULL,
    opt_type text DEFAULT ''::text NOT NULL,
    strike double precision,
    invest_ccy text DEFAULT ''::text NOT NULL,
    qty double precision NOT NULL,
    tenor_days integer,
    status text DEFAULT 'pending'::text NOT NULL,
    response text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    proposal_id bigint,
    settle_ccy text,
    settle_qty numeric(30,10),
    settle_income numeric(30,10),
    settled_at timestamp with time zone,
    CONSTRAINT fin_dci_order_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'filled'::text, 'failed'::text, 'rejected'::text, 'settled'::text])))
);


--
-- Name: fin_dci_order_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_dci_order_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_dci_order_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_dci_order_id_seq OWNED BY public.fin_dci_order.id;


--
-- Name: fin_employee_registry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_employee_registry (
    name text NOT NULL,
    role text DEFAULT ''::text NOT NULL,
    dept text DEFAULT ''::text NOT NULL,
    assembly text DEFAULT ''::text NOT NULL,
    cadence_sec integer DEFAULT 0 NOT NULL,
    running boolean DEFAULT false NOT NULL,
    exits integer DEFAULT 0 NOT NULL,
    task text DEFAULT ''::text NOT NULL,
    beat_ts timestamp with time zone,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_opp_template; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_opp_template (
    id text NOT NULL,
    mechanism text NOT NULL,
    net_apr text NOT NULL,
    exec_track text DEFAULT 'proposal'::text NOT NULL,
    risk_level text DEFAULT 'mid'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT fin_opp_template_exec_track_check CHECK ((exec_track = ANY (ARRAY['auto'::text, 'semi'::text, 'proposal'::text]))),
    CONSTRAINT fin_opp_template_risk_level_check CHECK ((risk_level = ANY (ARRAY['low'::text, 'mid'::text, 'high'::text])))
);


--
-- Name: fin_opp_template_cond; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_opp_template_cond (
    template_id text NOT NULL,
    seq integer NOT NULL,
    kind text NOT NULL,
    subject text NOT NULL,
    qualifier text DEFAULT ''::text NOT NULL,
    min_val double precision,
    CONSTRAINT fin_opp_template_cond_kind_check CHECK ((kind = ANY (ARRAY['credential'::text, 'capital'::text, 'permission'::text, 'constraint'::text, 'holding'::text])))
);


--
-- Name: fin_position_canonicals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_position_canonicals (
    position_id bigint NOT NULL,
    canonical text NOT NULL
);


--
-- Name: fin_proposal; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_proposal (
    id bigint NOT NULL,
    template_id text NOT NULL,
    exec_track text NOT NULL,
    venue text DEFAULT 'binance'::text NOT NULL,
    params jsonb DEFAULT '{}'::jsonb NOT NULL,
    expected_apr double precision,
    status text DEFAULT 'proposed'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    decided_at timestamp with time zone,
    CONSTRAINT fin_proposal_status_check CHECK ((status = ANY (ARRAY['proposed'::text, 'executed'::text, 'expired'::text, 'declined'::text, 'deferred'::text])))
);


--
-- Name: fin_proposal_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_proposal_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_proposal_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_proposal_id_seq OWNED BY public.fin_proposal.id;


--
-- Name: fin_symbol_aliases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_symbol_aliases (
    alias text NOT NULL,
    canonical text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_symdig_findings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_symdig_findings (
    run_ts timestamp with time zone NOT NULL,
    venue text NOT NULL,
    symbol text NOT NULL,
    klass text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL
);


--
-- Name: fin_trx_account; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_trx_account (
    id bigint NOT NULL,
    address character varying(64) NOT NULL,
    votes_total bigint NOT NULL,
    sr_address character varying(64),
    balance_sun bigint NOT NULL,
    allowance_sun bigint NOT NULL,
    energy_delegated bigint NOT NULL,
    bandwidth_staked bigint NOT NULL,
    net_limit bigint NOT NULL,
    unfrozen jsonb,
    fetched_at timestamp with time zone DEFAULT now() NOT NULL,
    tron_power bigint DEFAULT 0 NOT NULL,
    net_used bigint DEFAULT 0 NOT NULL,
    free_net_used bigint DEFAULT 0 NOT NULL,
    free_net_limit bigint DEFAULT 0 NOT NULL,
    energy_limit bigint DEFAULT 0 NOT NULL,
    energy_used bigint DEFAULT 0 NOT NULL,
    reward_num_sun bigint DEFAULT 0 NOT NULL
);


--
-- Name: COLUMN fin_trx_account.tron_power; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_trx_account.tron_power IS '质押量（sun，=bandwidthStaked+energyDelegated，除以 1e6 得 TRX）';


--
-- Name: COLUMN fin_trx_account.net_used; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_trx_account.net_used IS '已用带宽（sun，NetUsed，÷1e6 得 TRX）';


--
-- Name: COLUMN fin_trx_account.free_net_used; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_trx_account.free_net_used IS '免费已用带宽（sun，FreeNetUsed）';


--
-- Name: COLUMN fin_trx_account.free_net_limit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_trx_account.free_net_limit IS '免费带宽上限（sun，FreeNetLimit）';


--
-- Name: COLUMN fin_trx_account.energy_limit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_trx_account.energy_limit IS '能量上限（sun，EnergyLimit，÷1e6 得 TRX）';


--
-- Name: COLUMN fin_trx_account.energy_used; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_trx_account.energy_used IS '已用能量（sun，EnergyUsed，÷1e6 得 TRX）';


--
-- Name: fin_trx_account_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_trx_account_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_trx_account_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_trx_account_id_seq OWNED BY public.fin_trx_account.id;


--
-- Name: fin_trx_cycle_vi; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_trx_cycle_vi (
    id bigint NOT NULL,
    cycle bigint NOT NULL,
    sr_address character varying(64) NOT NULL,
    vi numeric DEFAULT 0 NOT NULL,
    user_votes bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_trx_cycle_vi_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_trx_cycle_vi_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_trx_cycle_vi_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_trx_cycle_vi_id_seq OWNED BY public.fin_trx_cycle_vi.id;


--
-- Name: fin_trx_txs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_trx_txs (
    id bigint NOT NULL,
    tx_id character varying(64) NOT NULL,
    ts timestamp with time zone NOT NULL,
    kind character varying(32) NOT NULL,
    amount_sun bigint NOT NULL,
    counterparty character varying(64),
    fetched_at timestamp with time zone DEFAULT now() NOT NULL,
    cheat_status boolean DEFAULT false NOT NULL
);


--
-- Name: fin_trx_txs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_trx_txs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_trx_txs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_trx_txs_id_seq OWNED BY public.fin_trx_txs.id;


--
-- Name: fin_trx_vote_apr; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_trx_vote_apr (
    id bigint NOT NULL,
    fetched_at timestamp with time zone DEFAULT now() NOT NULL,
    current_rate double precision DEFAULT 0 NOT NULL,
    current_sr character varying(64),
    max_rate double precision DEFAULT 0 NOT NULL,
    max_sr character varying(64),
    spread double precision DEFAULT 0 NOT NULL,
    min_rate double precision DEFAULT 0 NOT NULL,
    min_sr character varying(64)
);


--
-- Name: fin_trx_vote_apr_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_trx_vote_apr_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_trx_vote_apr_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_trx_vote_apr_id_seq OWNED BY public.fin_trx_vote_apr.id;


--
-- Name: fin_trx_vote_apr_top; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_trx_vote_apr_top (
    id bigint NOT NULL,
    fetched_at timestamp with time zone DEFAULT now() NOT NULL,
    sr_name character varying(64) NOT NULL,
    annualized_rate double precision NOT NULL
);


--
-- Name: fin_trx_vote_apr_top_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_trx_vote_apr_top_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_trx_vote_apr_top_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_trx_vote_apr_top_id_seq OWNED BY public.fin_trx_vote_apr_top.id;


--
-- Name: fin_trx_vote_rebalance; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_trx_vote_rebalance (
    id integer NOT NULL,
    decided_at timestamp with time zone DEFAULT now() NOT NULL,
    current_yield double precision NOT NULL,
    target_yield double precision NOT NULL,
    improvement_pct double precision NOT NULL,
    need_rebalance boolean NOT NULL,
    reason text NOT NULL,
    target_votes jsonb NOT NULL,
    executed_at timestamp with time zone,
    tx_hash text,
    status text DEFAULT 'pending'::text NOT NULL,
    fail_reason text
);


--
-- Name: fin_trx_vote_rebalance_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_trx_vote_rebalance_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_trx_vote_rebalance_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_trx_vote_rebalance_id_seq OWNED BY public.fin_trx_vote_rebalance.id;


--
-- Name: fin_trx_witness; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_trx_witness (
    id bigint NOT NULL,
    sr_address character varying(64) NOT NULL,
    url character varying(256),
    vote_count bigint,
    fetched_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_trx_witness_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_trx_witness_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_trx_witness_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_trx_witness_id_seq OWNED BY public.fin_trx_witness.id;


--
-- Name: fin_venue_account_snapshot; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_venue_account_snapshot (
    id bigint NOT NULL,
    venue text NOT NULL,
    venue_type text NOT NULL,
    balance_raw double precision,
    equity_raw double precision,
    free_margin_raw double precision,
    currency text DEFAULT ''::text NOT NULL,
    fetched_at timestamp with time zone DEFAULT now() NOT NULL,
    src text DEFAULT 'scheduler'::text NOT NULL,
    buffer_multiple double precision
);


--
-- Name: fin_venue_account_snapshot_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_venue_account_snapshot_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_venue_account_snapshot_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_venue_account_snapshot_id_seq OWNED BY public.fin_venue_account_snapshot.id;


--
-- Name: fin_venue_exposure_caps; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_venue_exposure_caps (
    venue_key text NOT NULL,
    cap_usd numeric NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT fin_venue_exposure_caps_cap_usd_check CHECK ((cap_usd >= (0)::numeric))
);


--
-- Name: fin_venue_io; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_venue_io (
    id bigint NOT NULL,
    venue text NOT NULL,
    config_id bigint NOT NULL,
    direction text NOT NULL,
    ccy text NOT NULL,
    amount numeric NOT NULL,
    fee numeric,
    status text,
    txid text,
    external_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    fetched_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_venue_io_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_venue_io_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_venue_io_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_venue_io_id_seq OWNED BY public.fin_venue_io.id;


--
-- Name: fin_venue_margin_status; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_venue_margin_status (
    venue text NOT NULL,
    venue_type text DEFAULT ''::text NOT NULL,
    tier text NOT NULL,
    coverage_days double precision,
    daily_vol_usd double precision,
    suggested_topup_usd double precision,
    last_alert_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: fin_venue_symbol_flags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_venue_symbol_flags (
    id bigint NOT NULL,
    symbol text NOT NULL,
    level text NOT NULL,
    reason text NOT NULL,
    kb_ref text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    venue_a text NOT NULL,
    venue_b text NOT NULL,
    venue text GENERATED ALWAYS AS (
CASE
    WHEN (venue_b = ''::text) THEN venue_a
    ELSE ((venue_a || '×'::text) || venue_b)
END) STORED,
    CONSTRAINT fin_venue_symbol_flags_level_check CHECK ((level = ANY (ARRAY['blocked'::text, 'warn'::text]))),
    CONSTRAINT vsf_venue_a_bare_chk CHECK (((venue_a <> ''::text) AND (venue_a !~~ '%:%'::text) AND (venue_a !~~ '%×%'::text))),
    CONSTRAINT vsf_venue_b_bare_chk CHECK (((venue_b !~~ '%:%'::text) AND (venue_b !~~ '%×%'::text)))
);


--
-- Name: COLUMN fin_venue_symbol_flags.venue_a; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_venue_symbol_flags.venue_a IS 'D-499 旗标源列·A 臂裸域（单 venue 旗=B 臂空）';


--
-- Name: COLUMN fin_venue_symbol_flags.venue_b; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_venue_symbol_flags.venue_b IS 'D-499 旗标源列·B 臂裸域（空串=单 venue 旗）';


--
-- Name: COLUMN fin_venue_symbol_flags.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.fin_venue_symbol_flags.venue IS 'D-499 展示性配对投影（generated：a 或 a×b，禁写）';


--
-- Name: fin_venue_symbol_flags_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_venue_symbol_flags_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_venue_symbol_flags_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_venue_symbol_flags_id_seq OWNED BY public.fin_venue_symbol_flags.id;


--
-- Name: fin_xasset_monitor; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.fin_xasset_monitor (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    pair text DEFAULT 'PAXG-XAU'::text NOT NULL,
    binance_bid numeric(20,8) NOT NULL,
    binance_ask numeric(20,8) NOT NULL,
    exness_bid numeric(20,8) NOT NULL,
    exness_ask numeric(20,8) NOT NULL,
    entry_spread_bps numeric(20,8) NOT NULL,
    exit_spread_bps numeric(20,8) NOT NULL,
    entry_net_bps numeric(20,8) NOT NULL,
    funding_rate_8h numeric(20,12) NOT NULL,
    funding_annual_pct numeric(10,4) NOT NULL,
    tracking_error_bps numeric(20,8) NOT NULL,
    equiv_annual_pct numeric(10,4) NOT NULL,
    noise_bps numeric,
    harvest_floor_bps numeric,
    src text DEFAULT 'live'::text NOT NULL,
    cex_venue text DEFAULT 'binance:perp'::text NOT NULL,
    mt_venue text DEFAULT '3001157:mt5'::text NOT NULL
);


--
-- Name: fin_xasset_monitor_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.fin_xasset_monitor_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: fin_xasset_monitor_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.fin_xasset_monitor_id_seq OWNED BY public.fin_xasset_monitor.id;


--
-- Name: idle_floor_streak; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.idle_floor_streak (
    venue text NOT NULL,
    venue_type text NOT NULL,
    consecutive_below_days integer DEFAULT 0 NOT NULL,
    last_check_day date NOT NULL,
    alerted boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: income_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.income_events (
    id bigint NOT NULL,
    venue text NOT NULL,
    exchange_config_id integer,
    symbol text NOT NULL,
    income_type text NOT NULL,
    amount_usd numeric(20,8) NOT NULL,
    tran_id bigint NOT NULL,
    ts timestamp with time zone NOT NULL,
    position_id integer,
    leg_no integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    venue_type text DEFAULT ''::text NOT NULL,
    venue_key text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type = ''::text) THEN venue
    ELSE ((venue || ':'::text) || venue_type)
END) STORED,
    CONSTRAINT income_events_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT income_events_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])))
);


--
-- Name: COLUMN income_events.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.income_events.venue IS 'R1 裸端点（CEX 交易所名）';


--
-- Name: COLUMN income_events.exchange_config_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.income_events.exchange_config_id IS 'R5 账户身份外键→exchange_configs.id（同 venue 多账户域下游标/唯一键按配置行区分）；NULL=历史遗留或账户未归属 exchange_configs 行';


--
-- Name: COLUMN income_events.symbol; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.income_events.symbol IS 'R4 原生品种名';


--
-- Name: COLUMN income_events.venue_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.income_events.venue_type IS 'R2 通道型';


--
-- Name: COLUMN income_events.venue_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.income_events.venue_key IS 'R3 生成投影';


--
-- Name: income_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.income_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: income_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.income_events_id_seq OWNED BY public.income_events.id;


--


--
-- Name: knowledge_entries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.knowledge_entries (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    signature text NOT NULL,
    symbol text DEFAULT ''::text NOT NULL,
    verdict text NOT NULL,
    rationale text DEFAULT ''::text NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    validated_at timestamp with time zone,
    validation_note text DEFAULT ''::text NOT NULL,
    venue_keys text[] DEFAULT '{}'::text[] NOT NULL
);


--
-- Name: COLUMN knowledge_entries.venue_keys; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.knowledge_entries.venue_keys IS 'venue_key 域数组（元素 venue:venue_type 或 CEX 裸名特例）';


--
-- Name: knowledge_entries_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.knowledge_entries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: knowledge_entries_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.knowledge_entries_id_seq OWNED BY public.knowledge_entries.id;


--
-- Name: live_gates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.live_gates (
    gate_id bigint NOT NULL,
    scope_type text NOT NULL,
    scope_key text DEFAULT ''::text NOT NULL,
    account_key text DEFAULT ''::text NOT NULL,
    strategy text DEFAULT ''::text NOT NULL,
    operator text NOT NULL,
    confirmed_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone,
    revoke_reason text DEFAULT ''::text NOT NULL,
    config_digest text DEFAULT ''::text NOT NULL,
    evidence_ref text DEFAULT ''::text NOT NULL,
    check_snapshot jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT live_gates_scope_type_check CHECK ((scope_type = ANY (ARRAY['global'::text, 'account'::text])))
);


--
-- Name: live_gates_gate_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.live_gates ALTER COLUMN gate_id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.live_gates_gate_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: macro_event; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.macro_event (
    id bigint NOT NULL,
    source text NOT NULL,
    event_time_utc timestamp with time zone NOT NULL,
    currency text NOT NULL,
    name text NOT NULL,
    importance text DEFAULT ''::text NOT NULL,
    forecast text DEFAULT ''::text NOT NULL,
    previous text DEFAULT ''::text NOT NULL,
    actual text DEFAULT ''::text NOT NULL,
    revision text DEFAULT ''::text NOT NULL,
    raw jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE macro_event; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.macro_event IS 'D-528：宏观财历事件（source=mql5；时间一律 UTC，渲染层转 BJ）';


--
-- Name: macro_event_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.macro_event_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: macro_event_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.macro_event_id_seq OWNED BY public.macro_event.id;


--
-- Name: opportunities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.opportunities (
    id text NOT NULL,
    type text NOT NULL,
    venue_buy text NOT NULL,
    venue_sell text NOT NULL,
    price_buy numeric(20,8) NOT NULL,
    price_sell numeric(20,8) NOT NULL,
    gross_bps numeric(20,8) NOT NULL,
    spread_cost numeric(20,8) DEFAULT 0 NOT NULL,
    commission_cost numeric(20,8) DEFAULT 0 NOT NULL,
    slippage_cost numeric(20,8) DEFAULT 0 NOT NULL,
    swap_cost numeric(20,8) DEFAULT 0 NOT NULL,
    net_bps numeric(20,8) NOT NULL,
    breakeven_days numeric(10,2),
    quote_time timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    canonical text,
    last_seen timestamp with time zone DEFAULT now() NOT NULL,
    asset_class text,
    canonical_sell text,
    venue_type_buy text DEFAULT ''::text NOT NULL,
    venue_type_sell text DEFAULT ''::text NOT NULL,
    venue_key_buy text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_buy = ''::text) THEN venue_buy
    ELSE ((venue_buy || ':'::text) || venue_type_buy)
END) STORED,
    venue_key_sell text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_sell = ''::text) THEN venue_sell
    ELSE ((venue_sell || ':'::text) || venue_type_sell)
END) STORED,
    CONSTRAINT opp_venue_bare_chk CHECK (((venue_buy !~~ '%:%'::text) AND (venue_sell !~~ '%:%'::text))),
    CONSTRAINT opp_venue_type_chk CHECK (((venue_type_buy = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])) AND (venue_type_sell = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])))),
    CONSTRAINT opportunities_type_check CHECK ((type = ANY (ARRAY['CROSS_VENUE'::text, 'CARRY'::text, 'FUNDING_HEDGE'::text, 'PERP_FUNDING'::text, 'XASSET_CARRY'::text])))
);


--
-- Name: COLUMN opportunities.venue_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.opportunities.venue_buy IS 'R1 裸端点（买腿）';


--
-- Name: COLUMN opportunities.venue_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.opportunities.venue_sell IS 'R1 裸端点（卖腿）';


--
-- Name: COLUMN opportunities.venue_type_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.opportunities.venue_type_buy IS 'R2 通道型（买腿）';


--
-- Name: COLUMN opportunities.venue_type_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.opportunities.venue_type_sell IS 'R2 通道型（卖腿）';


--
-- Name: COLUMN opportunities.venue_key_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.opportunities.venue_key_buy IS 'R3 生成投影（买腿）';


--
-- Name: COLUMN opportunities.venue_key_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.opportunities.venue_key_sell IS 'R3 生成投影（卖腿）';


--


--
-- Name: paper_spot_carry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.paper_spot_carry (
    id bigint NOT NULL,
    symbol text NOT NULL,
    side text NOT NULL,
    entry_funding_ann numeric NOT NULL,
    entry_ts timestamp with time zone DEFAULT now() NOT NULL,
    qty_usd numeric NOT NULL,
    last_funding_ann numeric,
    accrued_usd numeric DEFAULT 0 NOT NULL,
    last_accrue_ts timestamp with time zone,
    low_streak integer DEFAULT 0 NOT NULL,
    exit_ts timestamp with time zone,
    exit_reason text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    entry_basis_bps numeric,
    last_basis_bps numeric,
    structure text DEFAULT 'spot_carry'::text NOT NULL,
    mt_venue text,
    est_round_trip_fee_usd numeric,
    convergence_usd numeric,
    miss_streak integer DEFAULT 0 NOT NULL,
    accrue_ticks integer DEFAULT 0 NOT NULL,
    min_accrued_usd numeric,
    venue_a text NOT NULL,
    venue_b text DEFAULT ''::text NOT NULL,
    venue_type_a text DEFAULT ''::text NOT NULL,
    venue_type_b text DEFAULT ''::text NOT NULL,
    venue_key_a text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_a = ''::text) THEN venue_a
    ELSE ((venue_a || ':'::text) || venue_type_a)
END) STORED,
    venue_key_b text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_b = ''::text) THEN venue_b
    ELSE ((venue_b || ':'::text) || venue_type_b)
END) STORED,
    CONSTRAINT psc_venue_bare_chk CHECK (((venue_a !~~ '%:%'::text) AND (venue_b !~~ '%:%'::text) AND ((mt_venue IS NULL) OR (mt_venue !~~ '%:%'::text)))),
    CONSTRAINT psc_venue_type_ab_chk CHECK (((venue_type_a = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text])) AND (venue_type_b = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text]))))
);


--
-- Name: COLUMN paper_spot_carry.venue_a; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.paper_spot_carry.venue_a IS 'R1 裸端点 A 腿（MT=账号号，CEX=交易所名）';


--
-- Name: COLUMN paper_spot_carry.venue_b; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.paper_spot_carry.venue_b IS 'R1 裸端点 B 腿；''=历史单腿行';


--
-- Name: COLUMN paper_spot_carry.venue_type_a; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.paper_spot_carry.venue_type_a IS 'D-555 A 臂 venue_type（双CEX spot 腿=spot；mt×cex/存量行=空）';


--
-- Name: COLUMN paper_spot_carry.venue_type_b; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.paper_spot_carry.venue_type_b IS 'D-555 B 臂 venue_type（双CEX perp 腿=perp；mt×cex/存量行=空）';


--
-- Name: COLUMN paper_spot_carry.venue_key_a; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.paper_spot_carry.venue_key_a IS 'D-555 A 臂键域生成列（D-496 口径：type 空投影裸域）';


--
-- Name: COLUMN paper_spot_carry.venue_key_b; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.paper_spot_carry.venue_key_b IS 'D-555 B 臂键域生成列（D-496 口径：type 空投影裸域）';


--
-- Name: paper_spot_carry_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.paper_spot_carry_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: paper_spot_carry_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.paper_spot_carry_id_seq OWNED BY public.paper_spot_carry.id;


--
-- Name: personal_positions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.personal_positions (
    id bigint NOT NULL,
    asset text NOT NULL,
    kind text NOT NULL,
    price_symbol text NOT NULL,
    warn_high double precision,
    crit_high double precision,
    note text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'OPEN'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    closed_at timestamp with time zone,
    qty double precision,
    entry_price double precision,
    leverage integer,
    opened_at timestamp with time zone,
    cmc_instrument_id character varying(32),
    CONSTRAINT personal_positions_kind_check CHECK ((kind = ANY (ARRAY['staking'::text, 'short_hedge'::text, 'other'::text]))),
    CONSTRAINT personal_positions_status_check CHECK ((status = ANY (ARRAY['OPEN'::text, 'CLOSED'::text])))
);


--
-- Name: personal_positions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.personal_positions ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.personal_positions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: position_cashflows; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.position_cashflows (
    id bigint NOT NULL,
    position_id bigint NOT NULL,
    kind text NOT NULL,
    venue text NOT NULL,
    amount_usd double precision NOT NULL,
    ts timestamp with time zone NOT NULL,
    leg_no smallint,
    account_id bigint,
    raw_currency text,
    raw_amount double precision,
    fx_rate double precision,
    fx_source text,
    event_key text,
    external_event_id text,
    source text,
    quality text,
    source_ts timestamp with time zone,
    received_ts timestamp with time zone,
    occurred_at timestamp with time zone,
    venue_type text DEFAULT ''::text NOT NULL,
    venue_key text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type = ''::text) THEN venue
    ELSE ((venue || ':'::text) || venue_type)
END) STORED,
    CONSTRAINT position_cashflows_kind_check CHECK ((kind = ANY (ARRAY['funding'::text, 'swap'::text, 'commission'::text, 'other'::text]))),
    CONSTRAINT position_cashflows_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT position_cashflows_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])))
);


--
-- Name: COLUMN position_cashflows.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_cashflows.venue IS 'R1 裸端点';


--
-- Name: COLUMN position_cashflows.venue_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_cashflows.venue_type IS 'R2 通道型';


--
-- Name: COLUMN position_cashflows.venue_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_cashflows.venue_key IS 'R3 生成投影';


--
-- Name: position_cashflows_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.position_cashflows ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.position_cashflows_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: position_daily_income; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.position_daily_income (
    id bigint NOT NULL,
    day date NOT NULL,
    energy_trx numeric DEFAULT 0 NOT NULL,
    voting_trx numeric DEFAULT 0 NOT NULL,
    interest_usd numeric DEFAULT 0 NOT NULL,
    price numeric,
    pnl_usd numeric,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    source character varying(8) DEFAULT 'manual'::character varying NOT NULL
);


--
-- Name: position_daily_income_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.position_daily_income ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.position_daily_income_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: position_evals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.position_evals (
    id bigint NOT NULL,
    position_id bigint NOT NULL,
    evaluated_at timestamp with time zone DEFAULT now() NOT NULL,
    basis_bps double precision,
    anchor_bps double precision,
    fill_bps double precision,
    decision text NOT NULL,
    detail text DEFAULT ''::text NOT NULL
);


--
-- Name: position_evals_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.position_evals_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: position_evals_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.position_evals_id_seq OWNED BY public.position_evals.id;


--
-- Name: position_legs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.position_legs (
    id bigint NOT NULL,
    position_id bigint NOT NULL,
    leg_no smallint NOT NULL,
    phase text NOT NULL,
    venue text NOT NULL,
    symbol text NOT NULL,
    side text NOT NULL,
    qty double precision NOT NULL,
    price_expected double precision NOT NULL,
    price_actual double precision,
    order_id text DEFAULT ''::text NOT NULL,
    status text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    attempt_no smallint DEFAULT 1 NOT NULL,
    parent_attempt_id bigint,
    client_order_id text DEFAULT ''::text NOT NULL,
    requested_qty double precision,
    filled_qty double precision,
    avg_fill_price double precision,
    fee_raw text DEFAULT ''::text NOT NULL,
    fee_usd double precision,
    submitted_at timestamp with time zone,
    uncertain_at timestamp with time zone,
    venue_type text DEFAULT ''::text NOT NULL,
    venue_key text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type = ''::text) THEN venue
    ELSE ((venue || ':'::text) || venue_type)
END) STORED,
    fill_kind text,
    fail_reason text,
    CONSTRAINT position_legs_phase_check CHECK ((phase = ANY (ARRAY['OPEN'::text, 'CLOSE'::text]))),
    CONSTRAINT position_legs_side_check CHECK ((side = ANY (ARRAY['BUY'::text, 'SELL'::text]))),
    CONSTRAINT position_legs_status_check CHECK ((status = ANY (ARRAY['PENDING'::text, 'SUBMITTED'::text, 'FILLED'::text, 'FAILED'::text, 'UNKNOWN'::text, 'EXTERNAL'::text]))),
    CONSTRAINT position_legs_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT position_legs_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])))
);


--
-- Name: COLUMN position_legs.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_legs.venue IS 'R1 裸端点';


--
-- Name: COLUMN position_legs.symbol; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_legs.symbol IS 'R4 原生品种名（venue 侧原生形）';


--
-- Name: COLUMN position_legs.venue_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_legs.venue_type IS 'R2 通道型（''=非通道历史空值）';


--
-- Name: COLUMN position_legs.venue_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_legs.venue_key IS 'R3 生成投影，业务侧复合键读此列';


--
-- Name: position_legs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.position_legs ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.position_legs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: position_reviews; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.position_reviews (
    position_id bigint NOT NULL,
    hypothesis_id text,
    expected_net_usd double precision,
    actual_net_usd double precision,
    expected_cost_usd double precision,
    actual_cost_usd double precision,
    cost_deviation_usd double precision,
    expected_risk_usd double precision,
    actual_risk_usd double precision,
    risk_deviation_usd double precision,
    followup_action text,
    review_note text,
    reviewed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: position_settlements; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.position_settlements (
    position_id bigint NOT NULL,
    vkey text,
    expected_usd double precision,
    realized_usd double precision,
    notional_usd double precision,
    hold_days double precision,
    skipped_reason text,
    settled_at timestamp with time zone DEFAULT now() NOT NULL,
    price_pnl_usd double precision,
    cashflows_net_usd double precision,
    net_realized_usd double precision,
    pnl_quality text,
    completeness_status text,
    stats_status text,
    stats_updated_at timestamp with time zone,
    settlement_model_version integer,
    settlement_event_key text,
    incident_attributed boolean DEFAULT false NOT NULL,
    CONSTRAINT settlement_completeness_check CHECK (((completeness_status IS NULL) OR (completeness_status = ANY (ARRAY['complete'::text, 'partial'::text, 'skipped'::text, 'unknown'::text, 'failed'::text, 'legacy'::text])))),
    CONSTRAINT settlement_pnl_quality_check CHECK (((pnl_quality IS NULL) OR (pnl_quality = ANY (ARRAY['actual'::text, 'estimated'::text, 'legacy'::text, 'unknown'::text])))),
    CONSTRAINT settlement_stats_status_check CHECK (((stats_status IS NULL) OR (stats_status = ANY (ARRAY['pending'::text, 'applied'::text, 'retryable'::text, 'skipped'::text]))))
);


--
-- Name: position_swap_snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.position_swap_snapshots (
    id bigint NOT NULL,
    position_id bigint NOT NULL,
    venue text NOT NULL,
    swap_usd double precision NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    venue_type text DEFAULT ''::text NOT NULL,
    venue_key text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type = ''::text) THEN venue
    ELSE ((venue || ':'::text) || venue_type)
END) STORED,
    CONSTRAINT pss_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT pss_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])))
);


--
-- Name: COLUMN position_swap_snapshots.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_swap_snapshots.venue IS 'R1 裸端点';


--
-- Name: COLUMN position_swap_snapshots.venue_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_swap_snapshots.venue_type IS 'R2 通道型';


--
-- Name: COLUMN position_swap_snapshots.venue_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.position_swap_snapshots.venue_key IS 'R3 生成投影';


--
-- Name: position_swap_snapshots_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.position_swap_snapshots ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.position_swap_snapshots_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: positions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.positions (
    id bigint NOT NULL,
    opportunity_id text NOT NULL,
    type text NOT NULL,
    status text NOT NULL,
    expected_net_bps numeric(20,8),
    realized_pnl double precision,
    opened_at timestamp with time zone,
    closed_at timestamp with time zone,
    note text DEFAULT ''::text NOT NULL,
    expected_annual_pct numeric(10,4),
    expected_entry_spread_bps double precision,
    expected_swap_bps_daily double precision,
    request_id text,
    dedup_key text,
    environment text,
    state_version integer DEFAULT 1 NOT NULL,
    uncertainty_reason text,
    notional_usd double precision,
    closing_started_at timestamp with time zone,
    entry_noise_bps numeric,
    entry_harvest_floor_bps numeric,
    exit_kind text,
    fees_usd double precision,
    fee_quality text,
    decision_ref text,
    auto_close_at timestamp with time zone,
    CONSTRAINT positions_status_check CHECK ((status = ANY (ARRAY['OPENING'::text, 'OPEN'::text, 'LEGGED'::text, 'CLOSING'::text, 'CLOSED'::text, 'FAILED'::text]))),
    CONSTRAINT positions_type_check CHECK ((type = ANY (ARRAY['CROSS_VENUE'::text, 'CARRY'::text, 'FUNDING_HEDGE'::text, 'PERP_FUNDING'::text, 'SWAPARB'::text, 'MEANREVERT'::text, 'XASSET_CARRY'::text, 'PROBE'::text])))
);


--
-- Name: positions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.positions ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.positions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: radar_digest_member; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.radar_digest_member (
    kind text NOT NULL,
    member text NOT NULL,
    last_pushed_at timestamp with time zone,
    in_prev_set boolean NOT NULL
);


--
-- Name: radar_digest_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.radar_digest_state (
    kind text NOT NULL,
    content_hash text NOT NULL,
    pushed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: radar_watchlist; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.radar_watchlist (
    symbol text NOT NULL,
    venues text[] NOT NULL,
    reason text NOT NULL,
    first_seen timestamp with time zone DEFAULT now() NOT NULL,
    last_seen timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version integer NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: stats_anchor; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.stats_anchor (
    id integer DEFAULT 1 NOT NULL,
    anchor timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT stats_anchor_id_check CHECK ((id = 1))
);


--
-- Name: strategy_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.strategy_stats (
    strategy text NOT NULL,
    total_trades integer DEFAULT 0 NOT NULL,
    wins integer DEFAULT 0 NOT NULL,
    losses integer DEFAULT 0 NOT NULL,
    total_pnl double precision DEFAULT 0 NOT NULL,
    avg_pnl double precision DEFAULT 0 NOT NULL,
    win_rate double precision DEFAULT 0 NOT NULL,
    disabled boolean DEFAULT false NOT NULL,
    disabled_reason text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: swaparb_allow_combos; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.swaparb_allow_combos (
    id bigint NOT NULL,
    kind text NOT NULL,
    canonical text NOT NULL,
    venue_long text NOT NULL,
    venue_short text NOT NULL,
    max_long_qty numeric(20,8),
    max_short_qty numeric(20,8),
    max_gross_exposure_usd numeric(20,8),
    enabled boolean DEFAULT true NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT swaparb_allow_combos_kind_check CHECK ((kind = ANY (ARRAY['allow'::text, 'deny'::text])))
);


--
-- Name: TABLE swaparb_allow_combos; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.swaparb_allow_combos IS 'D-515：SWAPARB 组合门禁表化热读（configKick+60s 快照；env 字符串面停读）';


--
-- Name: COLUMN swaparb_allow_combos.kind; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.swaparb_allow_combos.kind IS 'allow/deny 双轨同表';


--
-- Name: COLUMN swaparb_allow_combos.canonical; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.swaparb_allow_combos.canonical IS '规范符号键';


--
-- Name: COLUMN swaparb_allow_combos.venue_long; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.swaparb_allow_combos.venue_long IS '买腿 venue_key（account:mt4/mt5）';


--
-- Name: COLUMN swaparb_allow_combos.venue_short; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.swaparb_allow_combos.venue_short IS '卖腿 venue_key';


--
-- Name: COLUMN swaparb_allow_combos.max_long_qty; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.swaparb_allow_combos.max_long_qty IS '买腿量上限（NULL/≤0=不限）';


--
-- Name: COLUMN swaparb_allow_combos.max_short_qty; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.swaparb_allow_combos.max_short_qty IS '卖腿量上限';


--
-- Name: COLUMN swaparb_allow_combos.max_gross_exposure_usd; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.swaparb_allow_combos.max_gross_exposure_usd IS '毛敞口上限 USD';


--
-- Name: COLUMN swaparb_allow_combos.enabled; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.swaparb_allow_combos.enabled IS '行开关（false=行不生效不删行）';


--
-- Name: swaparb_allow_combos_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.swaparb_allow_combos_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: swaparb_allow_combos_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.swaparb_allow_combos_id_seq OWNED BY public.swaparb_allow_combos.id;


--
-- Name: system_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.system_settings (
    id integer DEFAULT 1 NOT NULL,
    tronscan_base text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT system_settings_single_row CHECK ((id = 1))
);


--
-- Name: ticks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ticks (
    ts timestamp with time zone NOT NULL,
    venue text NOT NULL,
    symbol text NOT NULL,
    bid double precision NOT NULL,
    ask double precision NOT NULL,
    venue_type text NOT NULL,
    venue_key text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type = ''::text) THEN venue
    ELSE ((venue || ':'::text) || venue_type)
END) STORED,
    CONSTRAINT ticks_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT ticks_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text])))
)
PARTITION BY RANGE (ts);


--
-- Name: COLUMN ticks.venue; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.ticks.venue IS 'R1 裸端点（本表本就合规范式）';


--
-- Name: COLUMN ticks.venue_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.ticks.venue_type IS 'R2 通道型';


--
-- Name: COLUMN ticks.venue_key; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.ticks.venue_key IS 'R3 生成投影';


--
-- Name: ticks_2026_09; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ticks_2026_09 (
    ts timestamp with time zone NOT NULL,
    venue text NOT NULL,
    symbol text NOT NULL,
    bid double precision NOT NULL,
    ask double precision NOT NULL,
    venue_type text NOT NULL,
    venue_key text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type = ''::text) THEN venue
    ELSE ((venue || ':'::text) || venue_type)
END) STORED,
    CONSTRAINT ticks_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT ticks_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text])))
);


--
-- Name: ticks_2026_10; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ticks_2026_10 (
    ts timestamp with time zone NOT NULL,
    venue text NOT NULL,
    symbol text NOT NULL,
    bid double precision NOT NULL,
    ask double precision NOT NULL,
    venue_type text NOT NULL,
    venue_key text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type = ''::text) THEN venue
    ELSE ((venue || ':'::text) || venue_type)
END) STORED,
    CONSTRAINT ticks_venue_bare_chk CHECK ((venue !~~ '%:%'::text)),
    CONSTRAINT ticks_venue_type_chk CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text])))
);


--
-- Name: v_cycle_realrate; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_cycle_realrate AS
SELECT
    NULL::bigint AS position_id,
    NULL::text AS opportunity_id,
    NULL::text AS status,
    NULL::timestamp with time zone AS opened_at,
    NULL::timestamp with time zone AS closed_at,
    NULL::double precision AS realized_pnl,
    NULL::text AS exit_kind,
    NULL::double precision AS estimated_carry_usd,
    NULL::numeric AS simulated_carry_usd,
    NULL::double precision AS cycle_convergence_usd;


--
-- Name: verification_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.verification_stats (
    vkey text NOT NULL,
    samples integer DEFAULT 0 NOT NULL,
    expected_usd numeric DEFAULT 0 NOT NULL,
    realized_usd numeric DEFAULT 0 NOT NULL,
    last_sample_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: watchlist_symbols; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.watchlist_symbols (
    canonical text NOT NULL,
    sort integer DEFAULT 0 NOT NULL,
    added_at timestamp with time zone DEFAULT now() NOT NULL,
    venues text[]
);


--
-- Name: COLUMN watchlist_symbols.venues; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.watchlist_symbols.venues IS 'venue_key 域数组（元素 venue:venue_type），R3 契约声明';


--
-- Name: xasset_allow_combos; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.xasset_allow_combos (
    id bigint NOT NULL,
    canonical_buy text NOT NULL,
    canonical_sell text NOT NULL,
    venue_buy text NOT NULL,
    venue_sell text NOT NULL,
    max_long_qty numeric NOT NULL,
    max_short_qty numeric NOT NULL,
    max_gross_usd numeric NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    simulated boolean,
    note text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    notional_usd numeric,
    venue_type_buy text DEFAULT ''::text NOT NULL,
    venue_type_sell text DEFAULT ''::text NOT NULL,
    venue_key_buy text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_buy = ''::text) THEN venue_buy
    ELSE ((venue_buy || ':'::text) || venue_type_buy)
END) STORED,
    venue_key_sell text GENERATED ALWAYS AS (
CASE
    WHEN (venue_type_sell = ''::text) THEN venue_sell
    ELSE ((venue_sell || ':'::text) || venue_type_sell)
END) STORED,
    margin_usd numeric,
    CONSTRAINT xasset_allow_combos_budget_mutex CHECK ((NOT ((notional_usd IS NOT NULL) AND (margin_usd IS NOT NULL)))),
    CONSTRAINT xasset_allow_combos_margin_pos CHECK (((margin_usd IS NULL) OR (margin_usd > (0)::numeric))),
    CONSTRAINT xasset_allow_combos_max_gross_usd_check CHECK ((max_gross_usd > (0)::numeric)),
    CONSTRAINT xasset_allow_combos_max_long_qty_check CHECK ((max_long_qty > (0)::numeric)),
    CONSTRAINT xasset_allow_combos_max_short_qty_check CHECK ((max_short_qty > (0)::numeric)),
    CONSTRAINT xasset_venue_bare_chk CHECK (((venue_buy !~~ '%:%'::text) AND (venue_sell !~~ '%:%'::text))),
    CONSTRAINT xasset_venue_type_chk CHECK (((venue_type_buy = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text])) AND (venue_type_sell = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, ''::text]))))
);


--
-- Name: COLUMN xasset_allow_combos.venue_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.xasset_allow_combos.venue_buy IS 'R1 裸端点（买腿）';


--
-- Name: COLUMN xasset_allow_combos.venue_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.xasset_allow_combos.venue_sell IS 'R1 裸端点（卖腿）';


--
-- Name: COLUMN xasset_allow_combos.notional_usd; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.xasset_allow_combos.notional_usd IS 'D-367：组合级名义额（USD）；NULL=回退 ARB_OPP_NOTIONAL_USD 全局缺省';


--
-- Name: COLUMN xasset_allow_combos.venue_type_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.xasset_allow_combos.venue_type_buy IS 'R2 通道型（买腿）';


--
-- Name: COLUMN xasset_allow_combos.venue_type_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.xasset_allow_combos.venue_type_sell IS 'R2 通道型（卖腿）';


--
-- Name: COLUMN xasset_allow_combos.venue_key_buy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.xasset_allow_combos.venue_key_buy IS 'R3 生成投影（买腿）';


--
-- Name: COLUMN xasset_allow_combos.venue_key_sell; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.xasset_allow_combos.venue_key_sell IS 'R3 生成投影（卖腿）';


--
-- Name: COLUMN xasset_allow_combos.margin_usd; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.xasset_allow_combos.margin_usd IS 'D-010：组合保证金总预算 USD（双腿占用和上限）；NULL=未设；与 notional_usd 互斥';


--
-- Name: xasset_allow_combos_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.xasset_allow_combos_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: xasset_allow_combos_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.xasset_allow_combos_id_seq OWNED BY public.xasset_allow_combos.id;


--
-- Name: ticks_2026_09; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ticks ATTACH PARTITION public.ticks_2026_09 FOR VALUES FROM ('2026-09-01 00:00:00+00') TO ('2026-10-01 00:00:00+00');


--
-- Name: ticks_2026_10; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ticks ATTACH PARTITION public.ticks_2026_10 FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00');


--
-- Name: asset_equiv id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_equiv ALTER COLUMN id SET DEFAULT nextval('public.asset_equiv_id_seq'::regclass);


--
-- Name: autotrade_allow_rules id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.autotrade_allow_rules ALTER COLUMN id SET DEFAULT nextval('public.autotrade_allow_rules_id_seq'::regclass);


--
-- Name: carry_graduations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.carry_graduations ALTER COLUMN id SET DEFAULT nextval('public.carry_graduations_id_seq'::regclass);


--
-- Name: cex_private_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cex_private_events ALTER COLUMN id SET DEFAULT nextval('public.cex_private_events_id_seq'::regclass);


--
-- Name: fin_capability_constraint id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_capability_constraint ALTER COLUMN id SET DEFAULT nextval('public.fin_capability_constraint_id_seq'::regclass);


--
-- Name: fin_capability_permission id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_capability_permission ALTER COLUMN id SET DEFAULT nextval('public.fin_capability_permission_id_seq'::regclass);


--
-- Name: fin_carry_universe id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_carry_universe ALTER COLUMN id SET DEFAULT nextval('public.fin_carry_universe_id_seq'::regclass);


--
-- Name: fin_cex_dci_quote id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_dci_quote ALTER COLUMN id SET DEFAULT nextval('public.fin_cex_dci_quote_id_seq'::regclass);


--
-- Name: fin_cex_depth id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_depth ALTER COLUMN id SET DEFAULT nextval('public.fin_cex_depth_id_seq'::regclass);


--
-- Name: fin_cex_earn_apr id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_earn_apr ALTER COLUMN id SET DEFAULT nextval('public.fin_cex_earn_apr_id_seq'::regclass);


--
-- Name: fin_cex_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_events ALTER COLUMN id SET DEFAULT nextval('public.fin_cex_events_id_seq'::regclass);


--
-- Name: fin_cex_spread id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_spread ALTER COLUMN id SET DEFAULT nextval('public.fin_cex_spread_id_seq'::regclass);


--
-- Name: fin_condition_gap id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_condition_gap ALTER COLUMN id SET DEFAULT nextval('public.fin_condition_gap_id_seq'::regclass);


--
-- Name: fin_dci_order id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_dci_order ALTER COLUMN id SET DEFAULT nextval('public.fin_dci_order_id_seq'::regclass);


--
-- Name: fin_proposal id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_proposal ALTER COLUMN id SET DEFAULT nextval('public.fin_proposal_id_seq'::regclass);


--
-- Name: fin_trx_account id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_account ALTER COLUMN id SET DEFAULT nextval('public.fin_trx_account_id_seq'::regclass);


--
-- Name: fin_trx_cycle_vi id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_cycle_vi ALTER COLUMN id SET DEFAULT nextval('public.fin_trx_cycle_vi_id_seq'::regclass);


--
-- Name: fin_trx_txs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_txs ALTER COLUMN id SET DEFAULT nextval('public.fin_trx_txs_id_seq'::regclass);


--
-- Name: fin_trx_vote_apr id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_vote_apr ALTER COLUMN id SET DEFAULT nextval('public.fin_trx_vote_apr_id_seq'::regclass);


--
-- Name: fin_trx_vote_apr_top id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_vote_apr_top ALTER COLUMN id SET DEFAULT nextval('public.fin_trx_vote_apr_top_id_seq'::regclass);


--
-- Name: fin_trx_vote_rebalance id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_vote_rebalance ALTER COLUMN id SET DEFAULT nextval('public.fin_trx_vote_rebalance_id_seq'::regclass);


--
-- Name: fin_trx_witness id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_witness ALTER COLUMN id SET DEFAULT nextval('public.fin_trx_witness_id_seq'::regclass);


--
-- Name: fin_venue_account_snapshot id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_account_snapshot ALTER COLUMN id SET DEFAULT nextval('public.fin_venue_account_snapshot_id_seq'::regclass);


--
-- Name: fin_venue_io id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_io ALTER COLUMN id SET DEFAULT nextval('public.fin_venue_io_id_seq'::regclass);


--
-- Name: fin_venue_symbol_flags id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_symbol_flags ALTER COLUMN id SET DEFAULT nextval('public.fin_venue_symbol_flags_id_seq'::regclass);


--
-- Name: fin_xasset_monitor id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_xasset_monitor ALTER COLUMN id SET DEFAULT nextval('public.fin_xasset_monitor_id_seq'::regclass);


--
-- Name: income_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.income_events ALTER COLUMN id SET DEFAULT nextval('public.income_events_id_seq'::regclass);


--
-- Name: knowledge_entries id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_entries ALTER COLUMN id SET DEFAULT nextval('public.knowledge_entries_id_seq'::regclass);


--
-- Name: macro_event id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.macro_event ALTER COLUMN id SET DEFAULT nextval('public.macro_event_id_seq'::regclass);


--
-- Name: paper_spot_carry id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.paper_spot_carry ALTER COLUMN id SET DEFAULT nextval('public.paper_spot_carry_id_seq'::regclass);


--
-- Name: position_evals id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_evals ALTER COLUMN id SET DEFAULT nextval('public.position_evals_id_seq'::regclass);


--
-- Name: swaparb_allow_combos id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.swaparb_allow_combos ALTER COLUMN id SET DEFAULT nextval('public.swaparb_allow_combos_id_seq'::regclass);


--
-- Name: xasset_allow_combos id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.xasset_allow_combos ALTER COLUMN id SET DEFAULT nextval('public.xasset_allow_combos_id_seq'::regclass);


--
-- Name: ai_settings ai_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ai_settings
    ADD CONSTRAINT ai_settings_pkey PRIMARY KEY (id);


--
-- Name: alerts alerts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alerts
    ADD CONSTRAINT alerts_pkey PRIMARY KEY (id);


--
-- Name: app_config app_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_config
    ADD CONSTRAINT app_config_pkey PRIMARY KEY (key);


--
-- Name: app_secrets app_secrets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.app_secrets
    ADD CONSTRAINT app_secrets_pkey PRIMARY KEY (key);


--
-- Name: asset_equiv asset_equiv_asset_class_canonical_venue_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_equiv
    ADD CONSTRAINT asset_equiv_asset_class_canonical_venue_key UNIQUE (asset_class, canonical, venue);


--
-- Name: asset_equiv asset_equiv_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.asset_equiv
    ADD CONSTRAINT asset_equiv_pkey PRIMARY KEY (id);


--
-- Name: autotrade_allow_rules autotrade_allow_rules_kind_value_simulated_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.autotrade_allow_rules
    ADD CONSTRAINT autotrade_allow_rules_kind_value_simulated_key UNIQUE (kind, value, simulated);


--
-- Name: autotrade_allow_rules autotrade_allow_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.autotrade_allow_rules
    ADD CONSTRAINT autotrade_allow_rules_pkey PRIMARY KEY (id);


--
-- Name: autotrade_logs autotrade_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.autotrade_logs
    ADD CONSTRAINT autotrade_logs_pkey PRIMARY KEY (id);


--
-- Name: autotrade_state autotrade_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.autotrade_state
    ADD CONSTRAINT autotrade_state_pkey PRIMARY KEY (state);


--
-- Name: broker_accounts broker_accounts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.broker_accounts
    ADD CONSTRAINT broker_accounts_pkey PRIMARY KEY (id);


--
-- Name: broker_accounts broker_accounts_venue_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.broker_accounts
    ADD CONSTRAINT broker_accounts_venue_key UNIQUE (venue);


--
-- Name: broker_symbol_map broker_symbol_map_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.broker_symbol_map
    ADD CONSTRAINT broker_symbol_map_pkey PRIMARY KEY (id);


--
-- Name: broker_symbol_map broker_symbol_map_venue_broker_symbol_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.broker_symbol_map
    ADD CONSTRAINT broker_symbol_map_venue_broker_symbol_key UNIQUE (venue, broker_symbol);


--
-- Name: carry_graduations carry_graduations_paper_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.carry_graduations
    ADD CONSTRAINT carry_graduations_paper_id_key UNIQUE (paper_id);


--
-- Name: carry_graduations carry_graduations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.carry_graduations
    ADD CONSTRAINT carry_graduations_pkey PRIMARY KEY (id);


--
-- Name: cex_private_events cex_private_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cex_private_events
    ADD CONSTRAINT cex_private_events_pkey PRIMARY KEY (id);


--
-- Name: combo_daily_rollup combo_daily_rollup_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.combo_daily_rollup
    ADD CONSTRAINT combo_daily_rollup_pkey PRIMARY KEY (combo_key, day);


--
-- Name: exchange_configs exchange_configs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.exchange_configs
    ADD CONSTRAINT exchange_configs_pkey PRIMARY KEY (id);


--
-- Name: exchange_configs exchange_configs_venue_venue_type_simulated_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.exchange_configs
    ADD CONSTRAINT exchange_configs_venue_venue_type_simulated_key UNIQUE (venue, venue_type, simulated);


--
-- Name: execution_control_events execution_control_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.execution_control_events
    ADD CONSTRAINT execution_control_events_pkey PRIMARY KEY (id);


--
-- Name: execution_controls execution_controls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.execution_controls
    ADD CONSTRAINT execution_controls_pkey PRIMARY KEY (control_id);


--
-- Name: execution_controls execution_controls_scope_type_scope_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.execution_controls
    ADD CONSTRAINT execution_controls_scope_type_scope_key_key UNIQUE (scope_type, scope_key);


--
-- Name: explorer_proposals explorer_proposals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.explorer_proposals
    ADD CONSTRAINT explorer_proposals_pkey PRIMARY KEY (id);


--
-- Name: explorer_state explorer_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.explorer_state
    ADD CONSTRAINT explorer_state_pkey PRIMARY KEY (key);


--
-- Name: facts facts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.facts
    ADD CONSTRAINT facts_pkey PRIMARY KEY (id);


--
-- Name: feishu_push_target feishu_push_target_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.feishu_push_target
    ADD CONSTRAINT feishu_push_target_pkey PRIMARY KEY (id);


--
-- Name: fin_canonical_exposure_caps fin_canonical_exposure_caps_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_canonical_exposure_caps
    ADD CONSTRAINT fin_canonical_exposure_caps_pkey PRIMARY KEY (canonical);


--
-- Name: fin_capability_constraint fin_capability_constraint_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_capability_constraint
    ADD CONSTRAINT fin_capability_constraint_pkey PRIMARY KEY (id);


--
-- Name: fin_capability_permission fin_capability_permission_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_capability_permission
    ADD CONSTRAINT fin_capability_permission_pkey PRIMARY KEY (id);


--
-- Name: fin_capability_permission fin_capability_permission_venue_permission_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_capability_permission
    ADD CONSTRAINT fin_capability_permission_venue_permission_key UNIQUE (venue, permission);


--
-- Name: fin_carry_universe fin_carry_universe_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_carry_universe
    ADD CONSTRAINT fin_carry_universe_pkey PRIMARY KEY (id);


--
-- Name: fin_cex_dci_quote fin_cex_dci_quote_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_dci_quote
    ADD CONSTRAINT fin_cex_dci_quote_pkey PRIMARY KEY (id);


--
-- Name: fin_cex_depth fin_cex_depth_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_depth
    ADD CONSTRAINT fin_cex_depth_pkey PRIMARY KEY (id);


--
-- Name: fin_cex_earn_apr fin_cex_earn_apr_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_earn_apr
    ADD CONSTRAINT fin_cex_earn_apr_pkey PRIMARY KEY (id);


--
-- Name: fin_cex_earn_judgment fin_cex_earn_judgment_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_earn_judgment
    ADD CONSTRAINT fin_cex_earn_judgment_pkey PRIMARY KEY (id);


--
-- Name: fin_cex_events fin_cex_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_events
    ADD CONSTRAINT fin_cex_events_pkey PRIMARY KEY (id);


--
-- Name: fin_cex_spread fin_cex_spread_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_spread
    ADD CONSTRAINT fin_cex_spread_pkey PRIMARY KEY (id);


--
-- Name: fin_cex_universe fin_cex_universe_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_cex_universe
    ADD CONSTRAINT fin_cex_universe_pkey PRIMARY KEY (venue, symbol);


--
-- Name: fin_condition_gap fin_condition_gap_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_condition_gap
    ADD CONSTRAINT fin_condition_gap_pkey PRIMARY KEY (id);


--
-- Name: fin_condition_gap fin_condition_gap_template_id_kind_subject_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_condition_gap
    ADD CONSTRAINT fin_condition_gap_template_id_kind_subject_key UNIQUE (template_id, kind, subject);


--
-- Name: fin_dci_order fin_dci_order_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_dci_order
    ADD CONSTRAINT fin_dci_order_pkey PRIMARY KEY (id);


--
-- Name: fin_dci_order fin_dci_order_request_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_dci_order
    ADD CONSTRAINT fin_dci_order_request_id_key UNIQUE (request_id);


--
-- Name: fin_employee_registry fin_employee_registry_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_employee_registry
    ADD CONSTRAINT fin_employee_registry_pkey PRIMARY KEY (name);


--
-- Name: fin_opp_template_cond fin_opp_template_cond_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_opp_template_cond
    ADD CONSTRAINT fin_opp_template_cond_pkey PRIMARY KEY (template_id, seq);


--
-- Name: fin_opp_template fin_opp_template_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_opp_template
    ADD CONSTRAINT fin_opp_template_pkey PRIMARY KEY (id);


--
-- Name: fin_position_canonicals fin_position_canonicals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_position_canonicals
    ADD CONSTRAINT fin_position_canonicals_pkey PRIMARY KEY (position_id, canonical);


--
-- Name: fin_proposal fin_proposal_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_proposal
    ADD CONSTRAINT fin_proposal_pkey PRIMARY KEY (id);


--
-- Name: fin_symbol_aliases fin_symbol_aliases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_symbol_aliases
    ADD CONSTRAINT fin_symbol_aliases_pkey PRIMARY KEY (alias);


--
-- Name: fin_trx_account fin_trx_account_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_account
    ADD CONSTRAINT fin_trx_account_pkey PRIMARY KEY (id);


--
-- Name: fin_trx_cycle_vi fin_trx_cycle_vi_cycle_sr_address_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_cycle_vi
    ADD CONSTRAINT fin_trx_cycle_vi_cycle_sr_address_key UNIQUE (cycle, sr_address);


--
-- Name: fin_trx_cycle_vi fin_trx_cycle_vi_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_cycle_vi
    ADD CONSTRAINT fin_trx_cycle_vi_pkey PRIMARY KEY (id);


--
-- Name: fin_trx_txs fin_trx_txs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_txs
    ADD CONSTRAINT fin_trx_txs_pkey PRIMARY KEY (id);


--
-- Name: fin_trx_txs fin_trx_txs_tx_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_txs
    ADD CONSTRAINT fin_trx_txs_tx_id_key UNIQUE (tx_id);


--
-- Name: fin_trx_vote_apr fin_trx_vote_apr_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_vote_apr
    ADD CONSTRAINT fin_trx_vote_apr_pkey PRIMARY KEY (id);


--
-- Name: fin_trx_vote_apr_top fin_trx_vote_apr_top_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_vote_apr_top
    ADD CONSTRAINT fin_trx_vote_apr_top_pkey PRIMARY KEY (id);


--
-- Name: fin_trx_vote_rebalance fin_trx_vote_rebalance_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_vote_rebalance
    ADD CONSTRAINT fin_trx_vote_rebalance_pkey PRIMARY KEY (id);


--
-- Name: fin_trx_witness fin_trx_witness_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_trx_witness
    ADD CONSTRAINT fin_trx_witness_pkey PRIMARY KEY (id);


--
-- Name: fin_venue_account_snapshot fin_venue_account_snapshot_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_account_snapshot
    ADD CONSTRAINT fin_venue_account_snapshot_pkey PRIMARY KEY (id);


--
-- Name: fin_venue_exposure_caps fin_venue_exposure_caps_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_exposure_caps
    ADD CONSTRAINT fin_venue_exposure_caps_pkey PRIMARY KEY (venue_key);


--
-- Name: fin_venue_io fin_venue_io_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_io
    ADD CONSTRAINT fin_venue_io_pkey PRIMARY KEY (id);


--
-- Name: fin_venue_io fin_venue_io_venue_config_id_direction_external_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_io
    ADD CONSTRAINT fin_venue_io_venue_config_id_direction_external_id_key UNIQUE (venue, config_id, direction, external_id);


--
-- Name: fin_venue_margin_status fin_venue_margin_status_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_margin_status
    ADD CONSTRAINT fin_venue_margin_status_pkey PRIMARY KEY (venue, venue_type);


--
-- Name: fin_venue_symbol_flags fin_venue_symbol_flags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_venue_symbol_flags
    ADD CONSTRAINT fin_venue_symbol_flags_pkey PRIMARY KEY (id);


--
-- Name: fin_xasset_monitor fin_xasset_monitor_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_xasset_monitor
    ADD CONSTRAINT fin_xasset_monitor_pkey PRIMARY KEY (id);


--
-- Name: idle_floor_streak idle_floor_streak_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.idle_floor_streak
    ADD CONSTRAINT idle_floor_streak_pkey PRIMARY KEY (venue, venue_type);


--
-- Name: income_events income_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.income_events
    ADD CONSTRAINT income_events_pkey PRIMARY KEY (id);


--
-- Name: income_events income_events_tran_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.income_events
    ADD CONSTRAINT income_events_tran_uniq UNIQUE (exchange_config_id, tran_id);


--
-- Name: knowledge_entries knowledge_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_entries
    ADD CONSTRAINT knowledge_entries_pkey PRIMARY KEY (id);


--
-- Name: knowledge_entries knowledge_entries_signature_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_entries
    ADD CONSTRAINT knowledge_entries_signature_key UNIQUE (signature);


--
-- Name: live_gates live_gates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.live_gates
    ADD CONSTRAINT live_gates_pkey PRIMARY KEY (gate_id);


--
-- Name: live_gates live_gates_scope_type_scope_key_account_key_strategy_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.live_gates
    ADD CONSTRAINT live_gates_scope_type_scope_key_account_key_strategy_key UNIQUE (scope_type, scope_key, account_key, strategy);


--
-- Name: macro_event macro_event_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.macro_event
    ADD CONSTRAINT macro_event_pkey PRIMARY KEY (id);


--
-- Name: macro_event macro_event_source_event_time_utc_currency_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.macro_event
    ADD CONSTRAINT macro_event_source_event_time_utc_currency_name_key UNIQUE (source, event_time_utc, currency, name);


--
-- Name: opportunities opportunities_pkey1; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.opportunities
    ADD CONSTRAINT opportunities_pkey1 PRIMARY KEY (id);


--
-- Name: paper_spot_carry paper_spot_carry_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.paper_spot_carry
    ADD CONSTRAINT paper_spot_carry_pkey PRIMARY KEY (id);


--
-- Name: personal_positions personal_positions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.personal_positions
    ADD CONSTRAINT personal_positions_pkey PRIMARY KEY (id);


--
-- Name: position_cashflows position_cashflows_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_cashflows
    ADD CONSTRAINT position_cashflows_pkey PRIMARY KEY (id);


--
-- Name: position_daily_income position_daily_income_day_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_daily_income
    ADD CONSTRAINT position_daily_income_day_key UNIQUE (day);


--
-- Name: position_daily_income position_daily_income_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_daily_income
    ADD CONSTRAINT position_daily_income_pkey PRIMARY KEY (id);


--
-- Name: position_evals position_evals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_evals
    ADD CONSTRAINT position_evals_pkey PRIMARY KEY (id);


--
-- Name: position_legs position_legs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_legs
    ADD CONSTRAINT position_legs_pkey PRIMARY KEY (id);


--
-- Name: position_reviews position_reviews_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_reviews
    ADD CONSTRAINT position_reviews_pkey PRIMARY KEY (position_id);


--
-- Name: position_settlements position_settlements_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_settlements
    ADD CONSTRAINT position_settlements_pkey PRIMARY KEY (position_id);


--
-- Name: position_swap_snapshots position_swap_snapshots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_swap_snapshots
    ADD CONSTRAINT position_swap_snapshots_pkey PRIMARY KEY (id);


--
-- Name: positions positions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.positions
    ADD CONSTRAINT positions_pkey PRIMARY KEY (id);


--
-- Name: radar_digest_member radar_digest_member_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.radar_digest_member
    ADD CONSTRAINT radar_digest_member_pkey PRIMARY KEY (kind, member);


--
-- Name: radar_digest_state radar_digest_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.radar_digest_state
    ADD CONSTRAINT radar_digest_state_pkey PRIMARY KEY (kind);


--
-- Name: radar_watchlist radar_watchlist_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.radar_watchlist
    ADD CONSTRAINT radar_watchlist_pkey PRIMARY KEY (symbol);


--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);


--
-- Name: stats_anchor stats_anchor_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stats_anchor
    ADD CONSTRAINT stats_anchor_pkey PRIMARY KEY (id);


--
-- Name: strategy_stats strategy_stats_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.strategy_stats
    ADD CONSTRAINT strategy_stats_pkey PRIMARY KEY (strategy);


--
-- Name: swaparb_allow_combos swaparb_allow_combos_kind_canonical_venue_long_venue_short_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.swaparb_allow_combos
    ADD CONSTRAINT swaparb_allow_combos_kind_canonical_venue_long_venue_short_key UNIQUE (kind, canonical, venue_long, venue_short);


--
-- Name: swaparb_allow_combos swaparb_allow_combos_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.swaparb_allow_combos
    ADD CONSTRAINT swaparb_allow_combos_pkey PRIMARY KEY (id);


--
-- Name: system_settings system_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.system_settings
    ADD CONSTRAINT system_settings_pkey PRIMARY KEY (id);


--
-- Name: verification_stats verification_stats_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.verification_stats
    ADD CONSTRAINT verification_stats_pkey PRIMARY KEY (vkey);


--
-- Name: watchlist_symbols watchlist_symbols_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.watchlist_symbols
    ADD CONSTRAINT watchlist_symbols_pkey PRIMARY KEY (canonical);


--
-- Name: xasset_allow_combos xasset_allow_combos_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.xasset_allow_combos
    ADD CONSTRAINT xasset_allow_combos_pkey PRIMARY KEY (id);


--
-- Name: xasset_allow_combos xasset_allow_combos_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.xasset_allow_combos
    ADD CONSTRAINT xasset_allow_combos_uniq UNIQUE (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell);


--
-- Name: alerts_position_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX alerts_position_id_idx ON public.alerts USING btree (position_id) WHERE (position_id IS NOT NULL);


--
-- Name: alerts_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX alerts_ts_idx ON public.alerts USING btree (ts);


--
-- Name: autotrade_logs_strategy_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX autotrade_logs_strategy_idx ON public.autotrade_logs USING btree (strategy, ts DESC);


--
-- Name: autotrade_logs_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX autotrade_logs_ts_idx ON public.autotrade_logs USING btree (ts DESC);


--
-- Name: broker_symbol_map_primary_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX broker_symbol_map_primary_uniq ON public.broker_symbol_map USING btree (venue, canonical) WHERE (is_primary AND (canonical <> ''::text));


--
-- Name: execution_control_events_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX execution_control_events_key_idx ON public.execution_control_events USING btree (event_key);


--
-- Name: explorer_proposals_dedupe; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX explorer_proposals_dedupe ON public.explorer_proposals USING btree (scan_date, scan_kind, canonical, best_pair);


--
-- Name: facts_kind_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX facts_kind_ts_idx ON public.facts USING btree (kind, ts);


--
-- Name: facts_kind_venue_symbol_ts_desc_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX facts_kind_venue_symbol_ts_desc_idx ON public.facts USING btree (kind, venue, symbol, ts DESC);


--
-- Name: facts_null_symbol_kind_venue_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX facts_null_symbol_kind_venue_ts_idx ON public.facts USING btree (kind, venue, ts DESC) WHERE (symbol IS NULL);


--
-- Name: facts_venue_kind_symbol_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX facts_venue_kind_symbol_ts_idx ON public.facts USING btree (venue, kind, symbol, ts);


--
-- Name: facts_venuekey_kind_symbol_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX facts_venuekey_kind_symbol_ts_idx ON public.facts USING btree (venue_key, kind, symbol, ts);


--
-- Name: fin_capability_constraint_uq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX fin_capability_constraint_uq ON public.fin_capability_constraint USING btree (scope, COALESCE(position_id, (0)::bigint), subject);


--
-- Name: fin_carry_universe_net_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_carry_universe_net_idx ON public.fin_carry_universe USING btree (net_annual_pct DESC);


--
-- Name: fin_carry_universe_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_carry_universe_ts_idx ON public.fin_carry_universe USING btree (ts DESC);


--
-- Name: fin_proposal_pending_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX fin_proposal_pending_uniq ON public.fin_proposal USING btree (template_id) WHERE (status = 'proposed'::text);


--
-- Name: fin_proposal_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_proposal_status_idx ON public.fin_proposal USING btree (status, template_id);


--
-- Name: fin_symdig_findings_run_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_symdig_findings_run_idx ON public.fin_symdig_findings USING btree (run_ts);


--
-- Name: fin_trx_account_fetched_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_trx_account_fetched_idx ON public.fin_trx_account USING btree (fetched_at);


--
-- Name: fin_trx_cycle_vi_cycle_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_trx_cycle_vi_cycle_idx ON public.fin_trx_cycle_vi USING btree (cycle DESC, sr_address);


--
-- Name: fin_trx_txs_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_trx_txs_ts_idx ON public.fin_trx_txs USING btree (ts DESC);


--
-- Name: fin_trx_vote_apr_fetched_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_trx_vote_apr_fetched_idx ON public.fin_trx_vote_apr USING btree (fetched_at);


--
-- Name: fin_venue_account_snapshot_venue_fetched_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_venue_account_snapshot_venue_fetched_at_idx ON public.fin_venue_account_snapshot USING btree (venue, fetched_at DESC);


--
-- Name: fin_venue_symbol_flags_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX fin_venue_symbol_flags_active_idx ON public.fin_venue_symbol_flags USING btree (venue, symbol) WHERE (resolved_at IS NULL);


--
-- Name: idx_asset_equiv_class; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_asset_equiv_class ON public.asset_equiv USING btree (asset_class) WHERE enabled;


--
-- Name: idx_cex_earn_apr_venue_ccy; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cex_earn_apr_venue_ccy ON public.fin_cex_earn_apr USING btree (venue, ccy, ts DESC);


--
-- Name: idx_cex_earn_judgment_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cex_earn_judgment_ts ON public.fin_cex_earn_judgment USING btree (ts DESC);


--
-- Name: idx_cex_earn_judgment_venue_ccy_product; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_cex_earn_judgment_venue_ccy_product ON public.fin_cex_earn_judgment USING btree (venue, ccy, product_type);


--
-- Name: idx_cex_private_events_vcr; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cex_private_events_vcr ON public.cex_private_events USING btree (venue, channel, received_at DESC);


--
-- Name: idx_cex_spread_symbol_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cex_spread_symbol_ts ON public.fin_cex_spread USING btree (symbol, ts DESC);


--
-- Name: idx_dci_quote_vu_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_dci_quote_vu_ts ON public.fin_cex_dci_quote USING btree (venue, underlying, ts DESC);


--
-- Name: idx_fin_cex_depth_venue_symbol_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fin_cex_depth_venue_symbol_ts ON public.fin_cex_depth USING btree (venue, symbol, ts DESC);


--
-- Name: idx_fin_cex_events_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fin_cex_events_key ON public.fin_cex_events USING btree (event_type, symbol, ts DESC);


--
-- Name: idx_fin_cex_radar_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fin_cex_radar_ts ON public.fin_cex_radar USING btree (ts);


--
-- Name: idx_fin_venue_io_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_fin_venue_io_ts ON public.fin_venue_io USING btree (ts DESC);


--
-- Name: idx_macro_event_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_macro_event_time ON public.macro_event USING btree (event_time_utc);


--
-- Name: idx_opportunities_asset_class; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_opportunities_asset_class ON public.opportunities USING btree (asset_class) WHERE (asset_class IS NOT NULL);


--
-- Name: idx_opportunities_expires_new; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_opportunities_expires_new ON public.opportunities USING btree (expires_at DESC);


--
-- Name: idx_opportunities_type_quote_new; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_opportunities_type_quote_new ON public.opportunities USING btree (type, quote_time DESC);


--
-- Name: idx_positions_probe_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_positions_probe_due ON public.positions USING btree (auto_close_at) WHERE ((type = 'PROBE'::text) AND (status = ANY (ARRAY['OPEN'::text, 'LEGGED'::text])));


--
-- Name: idx_ticks_ts_brin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ticks_ts_brin ON ONLY public.ticks USING brin (ts);


--
-- Name: idx_ticks_venue_symbol_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ticks_venue_symbol_ts ON ONLY public.ticks USING btree (venue, symbol, ts);


--
-- Name: idx_trx_vote_apr_top_fetched_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_trx_vote_apr_top_fetched_at ON public.fin_trx_vote_apr_top USING btree (fetched_at DESC);


--
-- Name: idx_trx_vote_rebalance_decided_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_trx_vote_rebalance_decided_at ON public.fin_trx_vote_rebalance USING btree (decided_at);


--
-- Name: idx_trx_vote_rebalance_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_trx_vote_rebalance_status ON public.fin_trx_vote_rebalance USING btree (status);


--
-- Name: idx_xasset_monitor_pair_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_xasset_monitor_pair_ts ON public.fin_xasset_monitor USING btree (pair, ts DESC);


--
-- Name: idx_xasset_monitor_triple; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_xasset_monitor_triple ON public.fin_xasset_monitor USING btree (pair, cex_venue, mt_venue, ts DESC);


--
-- Name: idx_xasset_monitor_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_xasset_monitor_ts ON public.fin_xasset_monitor USING btree (ts DESC);


--
-- Name: income_events_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX income_events_ts_idx ON public.income_events USING btree (venue, ts);


--
-- Name: live_gates_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX live_gates_active_idx ON public.live_gates USING btree (scope_type, scope_key) WHERE (revoked_at IS NULL);


--
-- Name: paper_spot_carry_active_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX paper_spot_carry_active_uniq ON public.paper_spot_carry USING btree (venue_a, venue_b, symbol) WHERE (status = ANY (ARRAY['active'::text, 'demo'::text]));


--
-- Name: paper_spot_carry_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX paper_spot_carry_status_idx ON public.paper_spot_carry USING btree (status);


--
-- Name: personal_positions_symbol_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX personal_positions_symbol_idx ON public.personal_positions USING btree (price_symbol);


--
-- Name: position_cashflows_event_key_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX position_cashflows_event_key_uniq ON public.position_cashflows USING btree (event_key) WHERE (event_key IS NOT NULL);


--
-- Name: position_cashflows_per_leg_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX position_cashflows_per_leg_uniq ON public.position_cashflows USING btree (position_id, leg_no, kind, (((ts AT TIME ZONE 'UTC'::text))::date), COALESCE(event_key, ''::text)) WHERE (leg_no IS NOT NULL);


--
-- Name: position_evals_pos_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX position_evals_pos_ts ON public.position_evals USING btree (position_id, evaluated_at);


--
-- Name: position_legs_client_order_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX position_legs_client_order_id_idx ON public.position_legs USING btree (venue, client_order_id) WHERE (client_order_id <> ''::text);


--
-- Name: position_legs_pos_leg_phase_attempt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX position_legs_pos_leg_phase_attempt_idx ON public.position_legs USING btree (position_id, leg_no, phase, attempt_no);


--
-- Name: position_legs_position_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX position_legs_position_idx ON public.position_legs USING btree (position_id);


--
-- Name: position_swap_snapshots_pos_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX position_swap_snapshots_pos_ts_idx ON public.position_swap_snapshots USING btree (position_id, ts DESC);


--
-- Name: positions_active_dedup_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX positions_active_dedup_key_idx ON public.positions USING btree (dedup_key) WHERE (status = ANY (ARRAY['OPENING'::text, 'OPEN'::text, 'LEGGED'::text, 'CLOSING'::text]));


--
-- Name: positions_request_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX positions_request_id_idx ON public.positions USING btree (request_id) WHERE (request_id IS NOT NULL);


--
-- Name: positions_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX positions_status_idx ON public.positions USING btree (status, opened_at DESC);


--
-- Name: ticks_2026_09_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ticks_2026_09_ts_idx ON public.ticks_2026_09 USING brin (ts);


--
-- Name: ticks_2026_09_venue_symbol_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ticks_2026_09_venue_symbol_ts_idx ON public.ticks_2026_09 USING btree (venue, symbol, ts);


--
-- Name: ticks_2026_10_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ticks_2026_10_ts_idx ON public.ticks_2026_10 USING brin (ts);


--
-- Name: ticks_2026_10_venue_symbol_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ticks_2026_10_venue_symbol_ts_idx ON public.ticks_2026_10 USING btree (venue, symbol, ts);


--
-- Name: ticks_2026_09_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_ticks_ts_brin ATTACH PARTITION public.ticks_2026_09_ts_idx;


--
-- Name: ticks_2026_09_venue_symbol_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_ticks_venue_symbol_ts ATTACH PARTITION public.ticks_2026_09_venue_symbol_ts_idx;


--
-- Name: ticks_2026_10_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_ticks_ts_brin ATTACH PARTITION public.ticks_2026_10_ts_idx;


--
-- Name: ticks_2026_10_venue_symbol_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_ticks_venue_symbol_ts ATTACH PARTITION public.ticks_2026_10_venue_symbol_ts_idx;


--
-- Name: v_cycle_realrate _RETURN; Type: RULE; Schema: public; Owner: -
--

CREATE OR REPLACE VIEW public.v_cycle_realrate AS
 SELECT p.id AS position_id,
    p.opportunity_id,
    p.status,
    p.opened_at,
    p.closed_at,
    p.realized_pnl,
    p.exit_kind,
    COALESCE(sum(cf.amount_usd) FILTER (WHERE (cf.kind = ANY (ARRAY['swap'::text, 'funding'::text]))), (0)::double precision) AS estimated_carry_usd,
    COALESCE(( SELECT sum(ie.amount_usd) AS sum
           FROM public.income_events ie
          WHERE (ie.position_id = p.id)), (0)::numeric) AS simulated_carry_usd,
    (p.realized_pnl - (COALESCE(( SELECT sum(ie.amount_usd) AS sum
           FROM public.income_events ie
          WHERE (ie.position_id = p.id)), (0)::numeric))::double precision) AS cycle_convergence_usd
   FROM (public.positions p
     LEFT JOIN public.position_cashflows cf ON (((cf.position_id = p.id) AND (cf.leg_no IS NOT NULL))))
  GROUP BY p.id;


--
-- Name: strategy_stats strategy_stats_notify; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER strategy_stats_notify AFTER INSERT OR UPDATE OF disabled ON public.strategy_stats FOR EACH ROW EXECUTE FUNCTION public.strategy_stats_notify();


--
-- Name: alerts alerts_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.alerts
    ADD CONSTRAINT alerts_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.positions(id) ON DELETE SET NULL;


--
-- Name: broker_symbol_map broker_symbol_map_venue_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.broker_symbol_map
    ADD CONSTRAINT broker_symbol_map_venue_fk FOREIGN KEY (venue) REFERENCES public.broker_accounts(venue);


--
-- Name: carry_graduations carry_graduations_paper_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.carry_graduations
    ADD CONSTRAINT carry_graduations_paper_id_fkey FOREIGN KEY (paper_id) REFERENCES public.paper_spot_carry(id) ON DELETE CASCADE;


--
-- Name: fin_capability_constraint fin_capability_constraint_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_capability_constraint
    ADD CONSTRAINT fin_capability_constraint_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.personal_positions(id) ON DELETE SET NULL;


--
-- Name: fin_dci_order fin_dci_order_proposal_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_dci_order
    ADD CONSTRAINT fin_dci_order_proposal_id_fkey FOREIGN KEY (proposal_id) REFERENCES public.fin_proposal(id) ON DELETE SET NULL;


--
-- Name: fin_opp_template_cond fin_opp_template_cond_template_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_opp_template_cond
    ADD CONSTRAINT fin_opp_template_cond_template_id_fkey FOREIGN KEY (template_id) REFERENCES public.fin_opp_template(id) ON DELETE CASCADE;


--
-- Name: fin_position_canonicals fin_position_canonicals_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.fin_position_canonicals
    ADD CONSTRAINT fin_position_canonicals_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.positions(id) ON DELETE CASCADE;


--
-- Name: income_events income_events_config_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.income_events
    ADD CONSTRAINT income_events_config_fk FOREIGN KEY (exchange_config_id) REFERENCES public.exchange_configs(id);


--
-- Name: position_cashflows position_cashflows_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_cashflows
    ADD CONSTRAINT position_cashflows_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.positions(id) ON DELETE CASCADE;


--
-- Name: position_evals position_evals_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_evals
    ADD CONSTRAINT position_evals_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.positions(id);


--
-- Name: position_legs position_legs_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_legs
    ADD CONSTRAINT position_legs_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.positions(id) ON DELETE CASCADE;


--
-- Name: position_reviews position_reviews_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_reviews
    ADD CONSTRAINT position_reviews_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.positions(id);


--
-- Name: position_settlements position_settlements_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_settlements
    ADD CONSTRAINT position_settlements_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.positions(id);


--
-- Name: position_swap_snapshots position_swap_snapshots_position_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.position_swap_snapshots
    ADD CONSTRAINT position_swap_snapshots_position_id_fkey FOREIGN KEY (position_id) REFERENCES public.positions(id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--


