// accounts.tsx —— 账户管理：券商户 + 交易所配置。写径=对应 RPC；case16 增量。
import { useCallback, useEffect, useState } from "react";
import { client, errText } from "../api";
import { onCase } from "../stream";
import type { BrokerAccount, ExchangeConfig } from "../gen/dashboardv1/broker_admin_pb";
import type { AccountInfo } from "../gen/dashboardv1/account_pb";
import type { AccountEvent } from "../gen/dashboardv1/stream_pb";
import { fmtUsd, ts } from "../components/fmt";

const PLATFORMS = ["MT4", "MT5"];

export default function AccountsPage() {
  const [brokers, setBrokers] = useState<BrokerAccount[]>([]);
  const [exchs, setExchs] = useState<ExchangeConfig[]>([]);
  const [accts, setAccts] = useState<Map<string, AccountInfo>>(new Map());
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");

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
      {err && <div className="err">{err}</div>}
      {msg && <div className="good">{msg}</div>}

      <h2>券商账户（MT）</h2>
      <BrokerForm onDone={(m) => setMsg(m)} onErr={setErr} reload={load} />
      <table>
        <thead>
          <tr><th>venue</th><th>平台</th><th>主机</th><th>账号</th><th>别名</th><th>状态</th><th>权益</th><th>行情龄s</th><th>real</th><th>手续费bps</th><th></th></tr>
        </thead>
        <tbody>
          {brokers.map((b) => {
            const a = accts.get(b.venue);
            return (
              <tr key={b.venue}>
                <td>{b.venue}</td>
                <td>{PLATFORMS[b.platform] ?? b.platform}</td>
                <td>{b.host}:{b.port}</td>
                <td>{String(b.user)}</td>
                <td>
                  <AliasCell venue={b.venue} alias={b.alias} act={act} />
                </td>
                <td className={b.feedState === "live" ? "good" : "warn"}>{b.status}/{b.feedState}</td>
                <td>{a ? fmtUsd(a.equity) : "—"}</td>
                <td>{b.lastQuoteAgeSec.toFixed(0)}</td>
                <td>
                  <button onClick={() => act(() => client.setBrokerAccountIsReal({ venue: b.venue, isReal: !b.isReal }), "已切换 real")}>{String(b.isReal)}</button>
                </td>
                <td>
                  <input
                    style={{ width: 60 }}
                    defaultValue={b.commissionBps ?? ""}
                    onBlur={(e) =>
                      e.target.value !== String(b.commissionBps ?? "") &&
                      act(() => client.setBrokerCommission({ venue: b.venue, commissionBps: Number(e.target.value) }), "手续费已存")
                    }
                  />
                </td>
                <td>
                  <button onClick={() => act(() => client.setBrokerDisabled({ venue: b.venue, disabled: b.status !== "disabled" }), "已切换")}>{b.status === "disabled" ? "启用" : "停用"}</button>{" "}
                  <button className="danger" onClick={() => { if (confirm(`删户 ${b.venue}？`)) act(() => client.removeBroker({ venue: b.venue, reason: "ui" }), "已删户"); }}>删</button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>

      <h2>交易所（CEX）</h2>
      <ExchangeForm onDone={(m) => setMsg(m)} onErr={setErr} reload={load} />
      <table>
        <thead><tr><th>id</th><th>venue</th><th>类型</th><th>标签</th><th>simulated</th><th>创建</th><th></th></tr></thead>
        <tbody>
          {exchs.map((x) => (
            <tr key={x.id}>
              <td>{x.id}</td><td>{x.venue}</td><td>{x.venueType}</td>
              <td>
                <input
                  defaultValue={x.label}
                  onBlur={(e) =>
                    e.target.value !== x.label &&
                    act(() => client.setExchangeLabel({ id: x.id, label: e.target.value }), "标签已存")
                  }
                />
              </td>
              <td>{String(x.simulated)}</td>
              <td>{ts(x.createdAt)}</td>
              <td><button className="danger" onClick={() => { if (confirm(`删交易所 ${x.id}？`)) act(() => client.removeExchange({ id: x.id }), "已删"); }}>删</button></td>
            </tr>
          ))}
        </tbody>
      </table>
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
