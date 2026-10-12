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
