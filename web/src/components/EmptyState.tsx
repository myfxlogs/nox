// EmptyState 空态显式（段不消失语义——缺省文案调用方如实给）。
import type { ReactNode } from "react";
export function EmptyState({ children }: { children: ReactNode }) {
  return <div className="empty-state">{children}</div>;
}
