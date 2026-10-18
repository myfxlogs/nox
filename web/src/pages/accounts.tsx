// accounts.tsx —— 账户管理：券商户 + 交易所配置。写径=对应 RPC；case16 增量。
// S3′-final 批迁：第一眼=各 venue 户在通态+权益 opp-card 阵（卡内 别名/real/
// 手续费/停启/删 绑径原样）；新增表单挪段尾（高频看态低频加户）。纯 JSX/CSS
// 重排零逻辑动。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { BrokerAccount, ExchangeConfig } from "../gen/dashboardv1/broker_admin_pb";
import type { AccountInfo } from "../gen/dashboardv1/account_pb";
import type { AccountEvent } from "../gen/dashboardv1/stream_pb";
import { fmtUsd, ts } from "../components/fmt";
import { ConfirmDialog } from "../components/Modal";
import { MappingModal } from "../components/MappingModal";
import { AllowRulesSection } from "../components/AllowRulesSection";
import { XAssetCombosSection } from "../components/XAssetCombosSection";
import { WsMeters } from "../components/WsMeters";
import { Chip } from "../components/Chip";
import { EmptyState } from "../components/EmptyState";

const PLATFORMS = ["MT4", "MT5"];

export default function AccountsPage() {
  const [brokers, setBrokers] = useState<BrokerAccount[]>([]);
  const [exchs, setExchs] = useState<ExchangeConfig[]>([]);
  const [accts, setAccts] = useState<Map<string, AccountInfo>>(new Map());
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const [cfm, setCfm] = useState<{ text: string; run: () => void } | null>(null);
  const [mapVenue, setMapVenue] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [b, e, a] = await Promise.all([
        client.listBrokerAccounts({}),
        client.listExchanges({}),
        client.listAccounts({}),
      ]);
      setBrokers(b.accounts);
      setExchs(e.exchanges);
      setAccts(new Map(a.accounts.map((x) => [x.venueKey, x])));
      setErr("");
    } catch (e2) {
      setErr(errText(e2));
    }
  }, []);

  useEffect(() => {
    void load();
    const off = [
      onCase("accountEvent", (v) => {
        const a = (v as AccountEvent).account;
        if (a) setAccts((m) => new Map(m).set(a.venueKey, a));
      }),
      onCase("dataRefresh", () => void load()),
    ];
    return () => off.forEach((f) => f());
  }, [load]);

  async function act(fn: () => Promise<unknown>, ok: string) {
    try {
      await fn();
      setMsg(ok);
      void load();
    } catch (e) {
      setErr(errText(e));
    }
  }

  return (
    <div>
      <h1>账户</h1>
      {err && <div className="err">{err}</div>}
      {msg && <div className="good">{msg}</div>}

      {/* 第一眼=券商户在通态+权益（卡阵）；加户表单低频挪段尾 */}
      <h2>券商账户（MT）</h2>
      {brokers.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {brokers.map((b) => {
            const a = accts.get(b.venue);
            return (
              <li key={b.venue} className="opp-card">
                <div className="opp-card-head">
                  <span className="opp-card-kind">{b.venue}</span>
                  <Chip tone={b.feedState === "live" ? "good" : "warn"}>{b.status}/{b.feedState}</Chip>
                  <span className="opp-card-who">{PLATFORMS[b.platform] ?? b.platform} · {a ? fmtUsd(a.equity) : "—"}</span>
                </div>
                <div className="opp-card-narrative">
                  {b.host}:{b.port} · acct {String(b.user)} · 行情龄 {b.lastQuoteAgeSec.toFixed(0)}s
                </div>
                <div className="opp-card-narrative" style={{ display: "flex", gap: 6, flexWrap: "wrap", alignItems: "center" }}>
                  别名 <AliasCell venue={b.venue} alias={b.alias} act={act} />
                  手续费bps{" "}
                  <input
                    style={{ width: 56 }}
                    defaultValue={b.commissionBps ?? ""}
                    onBlur={(e) =>
                      e.target.value !== String(b.commissionBps ?? "") &&
                      act(() => client.setBrokerCommission({ venue: b.venue, commissionBps: Number(e.target.value) }), "手续费已存")
                    }
                  />
                </div>
                <div className="opp-card-narrative" style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                  <button onClick={() => act(() => client.setBrokerAccountIsReal({ venue: b.venue, isReal: !b.isReal }), "已切换 real")}>real={String(b.isReal)}</button>
                  <button onClick={() => act(() => client.setBrokerDisabled({ venue: b.venue, disabled: b.status !== "disabled" }), "已切换")}>{b.status === "disabled" ? "启用" : "停用"}</button>
                  <button onClick={() => setMapVenue(b.venue)}>映射</button>
                  <button className="danger" onClick={() => setCfm({ text: `删户 ${b.venue}？`, run: () => act(() => client.removeBroker({ venue: b.venue, reason: "ui" }), "已删户") })}>删</button>
                </div>
              </li>
            );
          })}
        </ul>
      ) : (
        <EmptyState>零券商户</EmptyState>
      )}
      <BrokerForm onDone={(m) => setMsg(m)} onErr={setErr} reload={load} />

      <h2>交易所（CEX）</h2>
      {exchs.length > 0 ? (
        <ul className="opp-cards opp-grid">
          {exchs.map((x) => (
            <li key={x.id} className="opp-card">
              <div className="opp-card-head">
                <span className="opp-card-kind">{x.venue}</span>
                <Chip tone="neutral">{x.venueType}</Chip>
                <span className="opp-card-who">#{x.id} · sim={String(x.simulated)}</span>
              </div>
              <div className="opp-card-narrative" style={{ display: "flex", gap: 6, alignItems: "center" }}>
                标签{" "}
                <input
                  defaultValue={x.label}
                  onBlur={(e) =>
                    e.target.value !== x.label &&
                    act(() => client.setExchangeLabel({ id: x.id, label: e.target.value }), "标签已存")
                  }
                />
                <button className="danger" onClick={() => setCfm({ text: `删交易所 ${x.id}？`, run: () => act(() => client.removeExchange({ id: x.id }), "已删") })}>删</button>
              </div>
              <div className="opp-card-narrative muted">创建 {ts(x.createdAt)}</div>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState>零交易所行</EmptyState>
      )}
      <ExchangeForm onDone={(m) => setMsg(m)} onErr={setErr} reload={load} />

      {/* 画骨③：符号映射浮层（broker_symbol_map 管理面——venue 级 scoped） */}
      {mapVenue && (
        <MappingModal venue={mapVenue} onClose={() => { setMapVenue(null); void load(); }}
          onErr={setErr} />
      )}
      {/* 画骨批二：护栏/组合/WS 计量三段（arb BrokersPage 族借形） */}
      <AllowRulesSection />
      <XAssetCombosSection />
      <WsMeters />

      <ConfirmDialog open={cfm != null} text={cfm?.text}
        onCancel={() => setCfm(null)}
        onConfirm={() => { const r = cfm?.run; setCfm(null); r?.(); }} />
    </div>
  );
}

function AliasCell(props: { venue: string; alias: string; act: (f: () => Promise<unknown>, m: string) => void }) {
  return (
    <input
      defaultValue={props.alias}
      onBlur={(e) =>
        e.target.value !== props.alias &&
        props.act(() => client.setBrokerAccountAlias({ venue: props.venue, alias: e.target.value }), "别名已存")
      }
    />
  );
}

function BrokerForm(p: { onDone: (m: string) => void; onErr: (m: string) => void; reload: () => void }) {
  const [f, setF] = useState({ venue: "", platform: "1", host: "", port: "", user: "", password: "", server: "" });
  async function add(e: React.FormEvent) {
    e.preventDefault();
    try {
      await client.addBroker({
        venue: f.venue, platform: Number(f.platform), host: f.host, port: Number(f.port),
        user: BigInt(f.user), password: f.password, server: f.server,
      });
      p.onDone("已添加");
      setF({ venue: "", platform: "1", host: "", port: "", user: "", password: "", server: "" });
      p.reload();
    } catch (ex) {
      p.onErr(errText(ex));
    }
  }
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    setF({ ...f, [k]: e.target.value });
  return (
    <form className="inline" onSubmit={add}>
      <input placeholder="venue" value={f.venue} onChange={set("venue")} required />
      <select value={f.platform} onChange={set("platform")}><option value="0">MT4</option><option value="1">MT5</option></select>
      <input placeholder="host" value={f.host} onChange={set("host")} required />
      <input placeholder="port" value={f.port} onChange={set("port")} required style={{ width: 70 }} />
      <input placeholder="user" value={f.user} onChange={set("user")} required />
      <input placeholder="password" type="password" value={f.password} onChange={set("password")} required />
      <input placeholder="server" value={f.server} onChange={set("server")} />
      <button type="submit">添加</button>
    </form>
  );
}

function ExchangeForm(p: { onDone: (m: string) => void; onErr: (m: string) => void; reload: () => void }) {
  const [f, setF] = useState({ venue: "", venueType: "", label: "", apiKey: "", apiSecret: "", passphrase: "", baseUrl: "" });
  async function add(e: React.FormEvent) {
    e.preventDefault();
    try {
      await client.addExchange({
        venue: f.venue, venueType: f.venueType, label: f.label, apiKey: f.apiKey,
        apiSecret: f.apiSecret, passphrase: f.passphrase, baseUrl: f.baseUrl,
      });
      p.onDone("已添加");
      setF({ venue: "", venueType: "", label: "", apiKey: "", apiSecret: "", passphrase: "", baseUrl: "" });
      p.reload();
    } catch (ex) {
      p.onErr(errText(ex));
    }
  }
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => setF({ ...f, [k]: e.target.value });
  return (
    <form className="inline" onSubmit={add}>
      <input placeholder="venue" value={f.venue} onChange={set("venue")} required />
      <input placeholder="类型 spot/perp" value={f.venueType} onChange={set("venueType")} />
      <input placeholder="标签" value={f.label} onChange={set("label")} />
      <input placeholder="api_key" value={f.apiKey} onChange={set("apiKey")} required />
      <input placeholder="api_secret" type="password" value={f.apiSecret} onChange={set("apiSecret")} required />
      <input placeholder="passphrase" value={f.passphrase} onChange={set("passphrase")} />
      <input placeholder="base_url" value={f.baseUrl} onChange={set("baseUrl")} />
      <button type="submit">添加</button>
    </form>
  );
}
