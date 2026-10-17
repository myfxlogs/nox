// collapse.tsx —— 折叠区件（业主令 10-17 弹窗化：点击标题=Modal 浮层非内联
// 展开——overlay 零 reflow 不顶页面高度）。defaultOpen=true=常驻展开段
// （语义「默认展开」保留旧形态——非点击展开面，不入弹窗域）。
import { useState, type ReactNode } from "react";
import { Modal } from "./Modal";

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
  const [open, setOpen] = useState(false);
  if (defaultOpen) {
    return (
      <section className="drawer" style={{ margin: "6px 0" }}>
        <div style={{ padding: "6px 8px" }}>
          <b>{title}</b>
          {hint && <span className="muted" style={{ float: "right" }}>{hint}</span>}
        </div>
        <div style={{ padding: "4px 8px 10px" }}>{children}</div>
      </section>
    );
  }
  return (
    <section className="drawer" style={{ margin: "6px 0" }}>
      <div
        onClick={() => setOpen(true)}
        style={{ cursor: "pointer", padding: "6px 8px" }}
      >
        <b>{title}</b>
        <span className="muted" style={{ float: "right" }}>
          {hint ? hint + " " : ""}▸
        </span>
      </div>
      <Modal open={open} onClose={() => setOpen(false)} title={title}>
        {children}
      </Modal>
    </section>
  );
}
