// main.tsx —— hash 路由 + 鉴权门（token 存 sessionStorage，未过→/login）。
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { useEffect, useState } from "react";
import { getToken } from "./api";
import { onStatus, onCase } from "./stream";
import { client } from "./api";
import { NOX_NAME } from "./nox";
import LoginPage from "./pages/login";
import OverviewPage from "./pages/overview";
import PositionsPage from "./pages/positions";
import AccountsPage from "./pages/accounts";
import RiskPage from "./pages/risk";
import OrgPage from "./pages/org";
import InvestorPage from "./pages/investor";
import TrxPage from "./pages/trx";
import CapitalPage from "./pages/capital";
import DciPage from "./pages/dci";
import SettingsPage from "./pages/settings";
import ReportsPage from "./pages/reports";
import WatchPage from "./pages/watch";
// S1′ 设计体系基建：app.css 旧轨暂并存 + arb styles/ 12 件整批挂入
// （逐页迁完再退役旧轨——双轨叠加期样式冲突出则以新 token 为准迭代修）。
import "./app.css";
import "./styles/00-base.css";
import "./styles/01-header-card.css";
import "./styles/02-collapse.css";
import "./styles/03-table-chip.css";
import "./styles/04-freshness-opp.css";
import "./styles/05-insights-review.css";
import "./styles/06-buttons.css";
import "./styles/07-bell-tabs.css";
import "./styles/08-facts-ledger.css";
import "./styles/09-sim.css";
import "./styles/10-testnet-quote.css";
import "./styles/11-account-responsive.css";
import "./styles/12-nox-shell.css";

// S2c 左导航两组分排（业主令 10-16）：门面=总览/投资人/持仓；
// 运维=账户/风控/观察/资金/DCI/报告/设置（盯盘归运维观察面）。
type RouteDef = { title: string; el: () => React.JSX.Element };
const NAV_GROUPS: { label: string; routes: [string, RouteDef][] }[] = [
  {
    label: "门面",
    routes: [
      ["/", { title: "总览", el: OverviewPage }],
      ["/investor", { title: "投资人", el: InvestorPage }],
      ["/positions", { title: "持仓", el: PositionsPage }],
      ["/trx", { title: "TRX", el: TrxPage }],
    ],
  },
  {
    label: "运维",
    routes: [
      ["/accounts", { title: "账户管理", el: AccountsPage }],
      ["/risk", { title: "风控健康", el: RiskPage }],
      ["/org", { title: "运行面", el: OrgPage }],
      ["/capital", { title: "资金地图", el: CapitalPage }],
      ["/dci", { title: "持仓理财", el: DciPage }],
      ["/reports", { title: "报告中心", el: ReportsPage }],
      ["/watch", { title: "盯盘", el: WatchPage }],
      ["/settings", { title: "设置", el: SettingsPage }],
    ],
  },
];
const ROUTES: Record<string, RouteDef> = Object.fromEntries(
  NAV_GROUPS.flatMap((g) => g.routes),
);

function useHash(): string {
  const [h, setH] = useState(location.hash.slice(1) || "/");
  useEffect(() => {
    const f = () => setH(location.hash.slice(1) || "/");
    addEventListener("hashchange", f);
    return () => removeEventListener("hashchange", f);
  }, []);
  return h;
}

// useActionBadge 投资人 nav 行动计数徽标（后端 GetActionBadge 供单值——
// 前端零计算令：禁聚合禁过滤，流事件驱动重取禁轮询）。
function useActionBadge(): number {
  const [n, setN] = useState(0);
  useEffect(() => {
    const load = async () => {
      try {
        const r = await client.getActionBadge({});
        setN(r.count);
      } catch { /* 徽标失败=静默降级不挡页 */ }
    };
    void load();
    const off = onCase("dataRefresh", () => void load());
    return off;
  }, []);
  return n;
}

function App() {
  const path = useHash();
  const [connected, setConnected] = useState(false);
  const badge = useActionBadge();
  useEffect(() => onStatus(setConnected), []);
  // S4 暗色备变量未启用——S2c 期间钉 light（arb 00-base 缺省即暗色须显式钉）。
  useEffect(() => { document.documentElement.dataset.theme = "light"; }, []);

  if (!getToken()) {
    if (path !== "/login") location.hash = "#/login";
    return <LoginPage />;
  }
  const r = ROUTES[path];
  if (!r) {
    location.hash = "#/"; // 默认首页=总览（高频看盘面，业主令）
    return null;
  }
  const El = r.el;
  return (
    <div className="nox-shell">
      <aside className="nox-side">
        <span className="brand">{NOX_NAME}</span>
        {NAV_GROUPS.map((g) => (
          <div key={g.label} className="nox-nav-group">
            <div className="grp-label">{g.label}</div>
            <nav>
              {g.routes.map(([p, v]) => (
                <a key={p} href={"#" + p} className={p === path ? "on" : ""}>
                  <span>{v.title}</span>
                  {p === "/investor" && badge > 0 ? <span className="nav-badge">{badge}</span> : null}
                </a>
              ))}
            </nav>
          </div>
        ))}
      </aside>
      <div className="nox-main">
        <header className="nox-topbar">
          <span className={connected ? "live" : "dead"}>
            {connected ? "● 已连接" : "○ 连接中"}
          </span>
        </header>
        <main className="shell">
          <El />
        </main>
      </div>
    </div>
  );
}

const root = document.getElementById("root");
if (root)
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>
  );
