// main.tsx —— hash 路由 + 鉴权门（token 存 sessionStorage，未过→/login）。
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { useEffect, useState } from "react";
import { getToken, clearToken } from "./api";
import { onStatus, onCase } from "./stream";
import { client } from "./api";
import { NOX_NAME } from "./nox";
import { Chip } from "./components/Chip";
import { FundingNeedToast } from "./components/FundingNeedToast";
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
import KnowledgePage from "./pages/knowledge";
import CexFinancePage from "./pages/cexfinance";
import CexScanPage from "./pages/cexscan";
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
import "./styles/11-personal.css";
import "./styles/12-nox-shell.css";
import "./styles/12-records.css";
import "./styles/20-semantic.css";
import "./styles/13-daily.css";
import "./styles/10-broker.css";
import "./styles/14-toast.css";
import "./styles/18-price-alert.css";
// arb 专属件差量合入（色彩差量迁移批 10-19）：19<20<21<22 数字序挂载，
// 22-mobile 的 .resp 表卡化/触控目标规则即活（HedgeFlow .resp 已消费）。
import "./styles/19-capital-coverage.css";
import "./styles/21-settings.css";
import "./styles/22-mobile.css";

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
      ["/knowledge", { title: "经验库", el: KnowledgePage }],
      ["/cex-finance", { title: "CEX 财务", el: CexFinancePage }],
      ["/cex-scan", { title: "CEX 扫描", el: CexScanPage }],
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
    if (!getToken()) return; // 未登录零发 authed RPC——预登录 401 会触发拦截器 reload（10-19 刷新循环病灶源头）
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

// ROUTE_ICONS —— 侧栏项 svg 路径（arb TAB_ICONS 借形语义对位；NOX 独项补
// user/shield/cpu/eye 同族 stroke 范式，viewBox 0 0 24 24 strokeWidth 2）。
const ROUTE_ICONS: Record<string, string> = {
  "/": "M3 12l9-9 9 9M5 10v10h14V10", // home
  "/investor": "M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2M12 3a4 4 0 1 0 0 8 4 4 0 0 0 0-8z", // user
  "/positions": "M4 4h16v16H4zM4 9h16M9 4v16", // table
  "/trx": "M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20M12 6v12M7 9l5-3 5 3", // coin
  "/accounts": "M12 2l3 7h7l-5.5 4 2 8L12 17l-6.5 4 2-8L2 9h7z", // star
  "/risk": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z", // shield
  "/org": "M4 4h16v16H4zM9 9h6v6H9zM9 1v3M15 1v3M9 20v3M15 20v3M1 9h3M1 15h3M20 9h3M20 15h3", // cpu
  "/capital": "M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6", // dollar
  "/dci": "M3 6h18v12H3zM3 10h18M7 14h4", // card
  "/reports": "M9 3h12v18H9M3 3h6v18H3M9 9h6M9 13h6M9 17h4", // book
  "/watch": "M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8zM12 12a3 3 0 1 0 0 .01z", // eye
  "/knowledge": "M4 19.5A2.5 2.5 0 0 1 6.5 17H20M4 19.5A2.5 2.5 0 0 0 6.5 22H20V2H6.5A2.5 2.5 0 0 0 4 4.5v15z",
  "/cex-finance": "M3 21h18M3 10h18M5 6l7-3 7 3M4 10v11M20 10v11M8 14v3M12 14v3M16 14v3",
  "/cex-scan": "M3 7V5a2 2 0 0 1 2-2h2M17 3h2a2 2 0 0 1 2 2v2M21 17v2a2 2 0 0 1-2 2h-2M7 21H5a2 2 0 0 1-2-2v-2M12 12a4 4 0 1 0 0 .01z",
  "/settings": "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM12 1v4M12 19v4M4.22 4.22l2.83 2.83M16.95 16.95l2.83 2.83M1 12h4M19 12h4M4.22 19.78l2.83-2.83M16.95 7.05l2.83-2.83", // gear
};

// logout 弃置本标签页 session 回登录卡（业主扫码验收后补件：无退出钮没法重测登录流）。
// 无服务端吊销面：单人 4h 短 session 设计，密钥级吊销=重生成 secret（/totp/setup 另票径）。
function logout() {
  clearToken();
  location.hash = "#/login";
  location.reload();
}

// 壳层钟面——票面裁准唯一合法 setInterval（UTC 钟面非业务数据）。
function ShellClock() {
  const [t, setT] = useState(() => new Date());
  useEffect(() => {
    const i = setInterval(() => setT(new Date()), 1000);
    return () => clearInterval(i);
  }, []);
  return <span className="muted">{t.toISOString().slice(11, 19)} UTC</span>;
}

function App() {
  const path = useHash();
  const [connected, setConnected] = useState(false);
  const badge = useActionBadge();
  const [sideOpen, setSideOpen] = useState(false);
  useEffect(() => onStatus(setConnected), []);
  // S4 暗色解锁（画骨样式批——00-base dark remap 完整实锤）：localStorage > 系统偏好，主题钮可换。
  const [theme, setTheme] = useState<"light" | "dark">(() => {
    const s = localStorage.getItem("nox-theme");
    return s === "light" || s === "dark" ? s
      : window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  });
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("nox-theme", theme);
  }, [theme]);

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
    <main className="app-layout">
      {/* arb 壳层范式：顶头条（汉堡+品牌+健康 chip+UTC 钟+主题钮）+图标侧栏+移动抽屉 */}
      <header className="app">
        <button className="sidebar-toggle" onClick={() => setSideOpen((o) => !o)} aria-label="菜单">
          <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M3 6h18M3 12h18M3 18h18" /></svg>
        </button>
        <span className="brand">{NOX_NAME}</span>
        <span className="app-spacer" />
        <Chip tone={connected ? "good" : "warn"}>{connected ? "已连接" : "连接中"}</Chip>
        <ShellClock />
        <button className="theme-btn" title="主题切换" onClick={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}>
          {theme === "dark" ? "☾" : "☀"}
        </button>
        <button className="theme-btn" title="退出登录" onClick={logout}>退出</button>
      </header>
      <nav className={`sidebar${sideOpen ? " open" : ""}`} aria-label="视图切换">
        {NAV_GROUPS.map((g) => (
          <div key={g.label}>
            <div className="grp-label">{g.label}</div>
            {g.routes.map(([p, v]) => (
              <a key={p} href={"#" + p}
                className={p === path ? "sidebar-item active" : "sidebar-item"}
                onClick={() => setSideOpen(false)}>
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <path d={ROUTE_ICONS[p] ?? "M4 4h16v16H4z"} />
                </svg>
                <span>{v.title}</span>
                {p === "/investor" && badge > 0 ? <span className="nav-badge">{badge}</span> : null}
              </a>
            ))}
          </div>
        ))}
      </nav>
      {sideOpen && <div className="sidebar-overlay" onClick={() => setSideOpen(false)} />}
      <div className="app-content">
        <El />
      </div>
      <FundingNeedToast />
    </main>
  );
}

const root = document.getElementById("root");
if (root)
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>
  );
