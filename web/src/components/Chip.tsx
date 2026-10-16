import type { ReactNode } from "react";
import { levelText, stateText } from "../format";

export type ChipTone = "neutral" | "good" | "warn" | "critical";

// Chip 状态徽标：点 + 文字标签（状态色永不带字面语义，见 style.css 注释）。
export function Chip({ tone, children, title }: { tone: ChipTone; children: ReactNode; title?: string }) {
  return (
    <span className={`chip chip-${tone}`} title={title}>
      <i className="dot" aria-hidden="true" />
      {children}
    </span>
  );
}

// levelChip 告警级别徽标：info 中性灰、warn/critical 用状态色。
export function LevelChip({ level }: { level: string }) {
  const tone: ChipTone = level === "warn" ? "warn" : level === "critical" ? "critical" : "neutral";
  return <Chip tone={tone}>{levelText(level)}</Chip>;
}

// stateChip 触发器状态徽标（armed 中性 / active 警示 / resolved 良好）。
export function StateChip({ state }: { state: string }) {
  const tone: ChipTone = state === "active" ? "warn" : state === "resolved" ? "good" : "neutral";
  return <Chip tone={tone}>{stateText(state)}</Chip>;
}
