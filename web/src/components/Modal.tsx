// Modal.tsx —— 自定义弹窗件（业主令 10-17：卡片点击出弹窗非内联展开）。
// overlay fixed 定位零 reflow（页面高度不变）；ESC/点遮罩/×钮三径关；
// 禁浏览器默认弹窗（window.alert/confirm/prompt 全仓零容忍）。
import { useEffect, type ReactNode } from "react";
import { createPortal } from "react-dom";

export function Modal({
  open,
  onClose,
  title,
  children,
  width = 720,
}: {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  children: ReactNode;
  width?: number;
}) {
  useEffect(() => {
    if (!open) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [open, onClose]);
  if (!open) return null;
  return createPortal(
    <div
      onClick={onClose}
      style={{
        position: "fixed", inset: 0, zIndex: 1000,
        background: "rgba(0,0,0,.55)",
        display: "flex", alignItems: "center", justifyContent: "center",
        padding: 16,
      }}
    >
      <div
        className="card"
        onClick={(e) => e.stopPropagation()}
        style={{
          width: "100%", maxWidth: width, maxHeight: "85vh",
          overflow: "auto", padding: "14px 16px", position: "relative",
        }}
      >
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 8 }}>
          <b>{title}</b>
          <button onClick={onClose} aria-label="关闭" style={{ fontSize: 16, lineHeight: 1 }}>×</button>
        </div>
        {children}
      </div>
    </div>,
    document.body,
  );
}

// ConfirmDialog 自定义确认弹窗（替 window.confirm——默认弹窗禁域）。
export function ConfirmDialog({
  open,
  text,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  text: ReactNode;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal open={open} onClose={onCancel} title="确认" width={420}>
      <div style={{ margin: "8px 0 14px" }}>{text}</div>
      <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
        <button onClick={onCancel}>取消</button>
        <button className="danger" onClick={onConfirm}>确认</button>
      </div>
    </Modal>
  );
}
