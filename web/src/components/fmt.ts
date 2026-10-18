// fmt.ts —— 展示格式化（纯渲染辅助，无业务口径）。
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

export function fmtUsd(v: number | null | undefined): string {
  if (v == null || Number.isNaN(v)) return "—";
  const s = v < 0 ? "-" : "";
  return `${s}$${Math.abs(v).toLocaleString("en-US", { maximumFractionDigits: 2 })}`;
}

export function ts(t: Timestamp | undefined): string {
  if (!t) return "—";
  return timestampDate(t).toLocaleString("zh-CN", { hour12: false });
}

export function tsUnix(u: bigint | number): string {
  const n = Number(u);
  if (!n) return "—";
  return new Date(n * 1000).toLocaleString("zh-CN", { hour12: false });
}

// fmtAge 请求龄展示（秒→紧凑中文龄；纯渲染格式化）。
export function fmtAge(sec: number): string {
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) return `${(sec / 3600).toFixed(1)}h`;
  return `${(sec / 86400).toFixed(1)}d`;
}

// fmtNum/fmtPrice/fmtThr/pnlClass —— arb PositionsPageHelpers 借形（画骨排版件共用）。
export function fmtNum(v: number, digits = 2): string {
  if (!v || !isFinite(v)) return "—";
  if (Math.abs(v) >= 1_000_000) return (v / 1_000_000).toFixed(2) + "M";
  if (Math.abs(v) >= 1_000) return (v / 1_000).toFixed(1) + "k";
  return v.toFixed(digits);
}
export function fmtPrice(v: number): string {
  return v > 0 ? v.toFixed(5) : "—";
}
export function fmtThr(v: number): string {
  return v > 0 ? v.toFixed(4) : "—";
}
// pnlClass：正负语义色（u-good/u-bad——20-semantic 全件已入）。
export function pnlClass(v: number, hasData: boolean): string {
  if (!hasData) return "muted";
  return v > 0 ? "u-good" : v < 0 ? "u-bad" : "muted";
}
export function fmtSigned(v: number, digits = 1): string {
  if (Math.abs(v) < 0.05) return "0";
  return parseFloat(v.toFixed(digits)).toString();
}
