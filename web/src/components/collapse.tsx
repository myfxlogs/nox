// collapse.tsx —— 折叠抽屉件（Zone 制，参照 /opt/arbcn Collapse 制式）：
// 原生 <details> 零 JS 依赖；title=标题、hint=行尾徽标、defaultOpen 定初始态。
import type { ReactNode } from "react";

export function Collapse({
  title,
  hint,
  defaultOpen = false,
  children,
}: {
  title: string;
  hint?: string;
  defaultOpen?: boolean;
  children: ReactNode;
}) {
  return (
    <details open={defaultOpen} className="drawer" style={{ margin: "6px 0" }}>
      <summary style={{ cursor: "pointer", padding: "6px 8px", listStyle: "none" }}>
        <b>{title}</b>
        {hint && <span className="muted" style={{ float: "right" }}>{hint}</span>}
      </summary>
      <div style={{ padding: "4px 8px 10px" }}>{children}</div>
    </details>
  );
}
