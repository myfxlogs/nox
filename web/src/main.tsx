// main.tsx —— hash 路由 + 鉴权门（token 存 sessionStorage，未过→/login）。
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { useEffect, useState } from "react";
import { getToken } from "./api";
import { onStatus } from "./stream";
import { NOX_NAME } from "./nox";
import LoginPage from "./pages/login";
import OverviewPage from "./pages/overview";
import PositionsPage from "./pages/positions";
import AccountsPage from "./pages/accounts";
import RiskPage from "./pages/risk";
import OrgPage from "./pages/org";
import InvestorPage from "./pages/investor";
import CapitalPage from "./pages/capital";
import DciPage from "./pages/dci";
import SettingsPage from "./pages/settings";
import ReportsPage from "./pages/reports";
import WatchPage from "./pages/watch";
import "./app.css";

const ROUTES: Record<string, { title: string; el: () => React.JSX.Element }> = {
  "/": { title: "总览", el: OverviewPage },
  "/positions": { title: "持仓", el: PositionsPage },
  "/accounts": { title: "账户管理", el: AccountsPage },
  "/risk": { title: "风控健康", el: RiskPage },
  "/org": { title: "运行面", el: OrgPage },
  "/investor": { title: "投资人", el: InvestorPage },
  "/capital": { title: "资金地图", el: CapitalPage },
  "/dci": { title: "持仓理财", el: DciPage },
  "/reports": { title: "报告中心", el: ReportsPage },
  "/watch": { title: "盯盘", el: WatchPage },
  "/settings": { title: "设置", el: SettingsPage },
};

function useHash(): string {
  const [h, setH] = useState(location.hash.slice(1) || "/");
  useEffect(() => {
    const f = () => setH(location.hash.slice(1) || "/");
    addEventListener("hashchange", f);
    return () => removeEventListener("hashchange", f);
  }, []);
  return h;
}

function App() {
  const path = useHash();
  const [connected, setConnected] = useState(false);
  useEffect(() => onStatus(setConnected), []);

  if (!getToken()) {
    if (path !== "/login") location.hash = "#/login";
    return <LoginPage />;
  }
  const r = ROUTES[path];
  if (!r) {
    location.hash = "#/";
    return null;
  }
  const El = r.el;
  return (
    <div className="shell">
      <header className="top">
        <span className="brand">{NOX_NAME}</span>
        <nav>
          {Object.entries(ROUTES).map(([p, v]) => (
            <a key={p} href={"#" + p} className={p === path ? "on" : ""}>
              {v.title}
            </a>
          ))}
        </nav>
        <span className={connected ? "live" : "dead"}>
          {connected ? "● 已连接" : "○ 连接中"}
        </span>
      </header>
      <main>
        <El />
      </main>
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
