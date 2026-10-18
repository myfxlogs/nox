// BrokerDetailModals.tsx —— Broker 余件两件（arb CredEditModal/AccountInfoModal
// 借形）：①凭据浮层（UpdateBrokerCredentials venue/host/port/server/password
// 全字段可改——空值域=传现值不遮假）；②账户信息浮层（AccountInfo 快照字段
// 直渲——零运算）。随件带入 10-broker.css 已挂（批二）。
import { useState } from "react";
import { client, errText } from "../api";
import { Modal } from "./Modal";
import { StatTile } from "./StatTile";
import type { AccountInfo } from "../gen/dashboardv1/account_pb";
import type { BrokerAccount } from "../gen/dashboardv1/broker_admin_pb";

// CredEditModal：券商连接凭据改设（host/port/server/password——venue 定死）。
export function CredEditModal({ b, onClose, onErr, onDone }: {
  b: BrokerAccount; onClose: () => void; onErr: (m: string) => void; onDone: (m: string) => void;
}) {
  const [f, setF] = useState({ host: b.host, port: String(b.port), server: b.server, password: "" });
  const [busy, setBusy] = useState(false);
  const save = async () => {
    setBusy(true);
    try {
      await client.updateBrokerCredentials({
        venue: b.venue, host: f.host, port: Number(f.port) || b.port,
        server: f.server, password: f.password,
      });
      onDone("凭据已存"); onClose();
    } catch (e) { onErr(errText(e)); } finally { setBusy(false); }
  };
  return (
    <Modal open onClose={onClose} title={`${b.venue} 连接凭据`} width={420}>
      <div style={{ display: "grid", gap: 8 }}>
        <label>host <input value={f.host} onChange={(e) => setF({ ...f, host: e.target.value })} /></label>
        <label>port <input type="number" value={f.port} onChange={(e) => setF({ ...f, port: e.target.value })} /></label>
        <label>server <input value={f.server} onChange={(e) => setF({ ...f, server: e.target.value })} /></label>
        <label>password <input type="password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} placeholder="空=不改" /></label>
        <div><button disabled={busy} onClick={() => void save()}>保存凭据</button> <button onClick={onClose}>取消</button></div>
      </div>
    </Modal>
  );
}

// AccountInfoModal：账户快照字段全透（金额/lever/margin/币种/时点——后端行直渲）。
export function AccountInfoModal({ a, onClose }: { a: AccountInfo; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={`${a.venueKey} 账户实况`} width={480}>
      <div className="stats">
        <StatTile label="权益" value={a.equity !== undefined ? a.equity.toFixed(2) : "—"} />
        <StatTile label="余额" value={a.balance !== undefined ? a.balance.toFixed(2) : "—"} />
        <StatTile label="可用保证金" value={a.freeMargin !== undefined ? a.freeMargin.toFixed(2) : "—"} />
        <StatTile label="杠杆" value={String(a.leverage)} />
        <StatTile label="币种" value={a.currency || "—"} />
        <StatTile label="在管" value={a.online ? "在线" : "快照"} />
      </div>
      <div className="muted" style={{ fontSize: 12, marginTop: 8 }}>
        {a.venueKey} · {a.assetType} · 更新 {a.updatedAt ? new Date(Number(a.updatedAt.seconds) * 1000).toLocaleString() : "—"}
      </div>
    </Modal>
  );
}
