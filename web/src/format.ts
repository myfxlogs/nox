import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

// pct 年化/百分比格式化（value 已按 % 口径存储）。
export const pct = (v: number, digits = 2): string => `${v.toFixed(digits)}%`;

// days 日历倒计时（Fact.Value = 距事件天数，当日 = 0）。
export const days = (v: number): string => `${Math.round(v)} 天`;

// num 触发器 last_value 通用显示（口径随规则而异，保留 4 位有效数字）。
export const num = (v: number): string => v.toPrecision(4);

// fmtTs 精确到分钟的时间戳；缺省 = "—"。
export const fmtTs = (t?: Timestamp): string =>
  t
    ? timestampDate(t).toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      })
    : "—";

// fmtClock 仅时分秒（顶部"更新于"）。
export const fmtClock = (d: Date): string =>
  d.toLocaleTimeString("zh-CN", { hour12: false });

// fmtRel 相对时间（铃铛条目时间 / freshness tooltip"最近更新 X 前"）。
export const fmtRel = (t?: Timestamp): string => {
  if (!t) return "—";
  const diff = Math.max(0, Date.now() - timestampDate(t).getTime());
  const m = Math.floor(diff / 60_000);
  if (m < 1) return "刚刚";
  if (m < 60) return `${m} 分钟前`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h} 小时前`;
  return `${Math.floor(h / 24)} 天前`;
};

// ledgerDate 把 datetime-local 字符串（YYYY-MM-DDTHH:mm）转 proto Timestamp
// （台账表单用；D-047 F5 从 hooks.ts 移入 format.ts——纯转换函数归置显示工具层）。
export const ledgerDate = (
  input: string,
): ReturnType<typeof timestampFromDate> | undefined => {
  if (!input) return undefined;
  const d = new Date(input);
  return Number.isNaN(d.getTime()) ? undefined : timestampFromDate(d);
};

// fmtInterval 源轮询间隔（秒 → 中文）。
export const fmtInterval = (sec: bigint): string => {
  const s = Number(sec);
  if (s % 3600 === 0 && s > 0) return `${s / 3600} 小时`;
  if (s % 60 === 0 && s > 0) return `${s / 60} 分钟`;
  return `${s} 秒`;
};

// statusText freshness 状态文案（M2-a §2.3：stale/down 区分业务含义；纯中文，无英文前缀）。
export const statusText = (status: string): string => {
  switch (status) {
    case "live":
      return "正常";
    case "stale":
      return "市场闭市/冻结";
    case "down":
      return "采集器失联";
    default:
      return status;
  }
};

// reasonText healthz 降级原因 → 中文（后端原因码为英文枚举，UI 层映射）。
export const reasonText = (reason: string): string => {
  switch (reason) {
    case "db_unreachable":
      return "数据库不可达";
    case "pending_migrations":
      return "迁移未完成";
    case "migrations_check_failed":
      return "迁移检查失败";
    default:
      return reason;
  }
};

// stateText 触发器状态机 → 中文（armed 待命 / active 触发 / resolved 已解除）。
export const stateText = (state: string): string => {
  switch (state) {
    case "armed":
      return "待命";
    case "active":
      return "触发";
    case "resolved":
      return "已解除";
    default:
      return state;
  }
};

// levelText 告警级别 → 中文（info 提示 / warn 警告 / critical 严重）。
export const levelText = (level: string): string => {
  switch (level) {
    case "info":
      return "提示";
    case "warn":
      return "警告";
    case "critical":
      return "严重";
    default:
      return level;
  }
};

// tierLabel 台账档位 → 中文（D-026 三档 + 持有层；entry 自带档位，不推断；
// 空 = 未分类；未知档位回退原名，演进预留）。
export const tierLabel = (tier: string): string => {
  switch (tier) {
    case "protected_convexity":
      return "保本凸性";
    case "stable_base":
      return "稳定币基档";
    case "cash_management":
      return "现金管理";
    case "holding":
      return "持有层";
    case "":
      return "未分类";
    default:
      return tier;
  }
};

// fmtAmount 台账金额（千分位 + 最多 2 位小数；正=入金，负=出金）。
export const fmtAmount = (v: number): string =>
  v.toLocaleString("zh-CN", { maximumFractionDigits: 2 });

// factValue 事实值按单位显示（快照 USD 原值 / RMB 视角共用）：pct_annualized/pct →
// 百分比；days → 天；price → 4 位小数；ratio → 4 位有效数字；未知单位 → 原值。
// 修 M2-b 审阅缺陷 F2：原实现对所有 kind 统一 pct()，导致 fx（price 6.744）显示
// "674.43%"、calendar（days 16）显示 "16.00%"。
export const factValue = (v: number, unit: string): string => {
  switch (unit) {
    case "pct_annualized":
    case "pct":
      return pct(v);
    case "days":
      return days(v);
    case "price":
      return v.toFixed(4);
    case "ratio":
      return v.toPrecision(4);
    default:
      return String(v);
  }
};

// unitText 事实单位码 → 中文（机会面板/快照展示）。
export const unitText = (unit: string): string => {
  switch (unit) {
    case "pct_annualized":
      return "年化 %";
    case "price":
      return "价格";
    case "pct":
      return "%";
    case "days":
      return "天";
    case "ratio":
      return "比值";
    default:
      return unit;
  }
};

// fxText 汇率可用性（M2-b §4：汇率缺失 → USD 原值 + 「汇率不可用」标记）。
export const fxText = (available: boolean): string =>
  available ? "已折算" : "汇率不可用";

// ruleLabel 规则名 → 中文标签（告警流/触发器展示；规则名本身是稳定标识符，
// 未知名回退原名，与后端 rule/ruleLabels 同语义）。
export const ruleLabel = (name: string): string => {
  switch (name) {
    case "funding_warn":
      return "资金费率预警";
    case "funding_critical":
      return "资金费率激活";
    case "trx_funding_positive":
      return "TRX 费率转正";
    case "defi_large_tier_change":
      return "金额档利率变动";
    case "ladder_trap":
      return "阶梯陷阱识别";
    case "reverse_repo_timing":
      return "逆回购时点";
    case "usdcnh_buy_line":
      return "汇率加仓线";
    case "iv_opportunity":
      return "IV 机会";
    case "nonstable_quote_change":
      return "计价币种陷阱";
    case "collector_heartbeat":
      return "采集器心跳";
    default:
      return name;
  }
};
