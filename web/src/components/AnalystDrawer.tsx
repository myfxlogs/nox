// AnalystDrawer.tsx —— 分析员抽屉（arb 同名件借形，流式 AskAnalyst 消费件）：
// FAB「✦ 分析员」挂顶头条 → 抽屉问答（delta 流式拼文/citation 引证/
// followUps 追问钮/done grounded 标），history 全轮回放；appendAnalystNote
// 「记一下→待归纳」径原样。envOff chip 关显示+关 page 发送=诚实。
import { useCallback, useEffect, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { client } from "../api";
import {
  AskAnalystRequestSchema, ChatTurnSchema,
  type AnalystCitation, type ChatTurn,
} from "../gen/dashboardv1/watchlist_analyst_pb";

const PAGE_LABEL: Record<string, string> = {
  "/": "总览", "/investor": "投资人", "/positions": "持仓", "/trx": "TRX",
  "/accounts": "账户管理", "/risk": "风控健康", "/org": "运行面",
  "/capital": "资金地图", "/dci": "持仓理财", "/reports": "报告中心",
  "/watch": "盯盘", "/settings": "设置",
};

type Turn = {
  question: string; answer: string; citations: AnalystCitation[];
  followUps: string[]; grounded: boolean; streaming: boolean; noted: boolean;
};

export function AnalystDrawer({ open, onClose, page }: { open: boolean; onClose: () => void; page: string }) {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [noting, setNoting] = useState<number | null>(null);
  const [envOff, setEnvOff] = useState(false);
  const bodyRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (bodyRef.current) bodyRef.current.scrollTop = bodyRef.current.scrollHeight;
  }, [turns]);
  useEffect(() => { if (open) setEnvOff(false); }, [open]);

  const ask = useCallback(async (question: string) => {
    if (!question.trim() || busy) return;
    const history: ChatTurn[] = turns.flatMap((t) => [
      create(ChatTurnSchema, { user: true, text: t.question }),
      create(ChatTurnSchema, { user: false, text: t.answer }),
    ]);
    const req = create(AskAnalystRequestSchema, { question, context: [], page: envOff ? "" : page, history });
    setTurns((prev) => [...prev, { question, answer: "", citations: [], followUps: [], grounded: false, streaming: true, noted: false }]);
    setInput(""); setBusy(true);
    try {
      const stream = await client.askAnalyst(req);
      for await (const msg of stream) {
        const p = msg.part;
        setTurns((prev) => {
          const next = [...prev];
          const last = next[next.length - 1];
          if (!last) return prev;
          if (p.case === "delta") next[next.length - 1] = { ...last, answer: last.answer + p.value };
          else if (p.case === "citation") next[next.length - 1] = { ...last, citations: [...last.citations, p.value] };
          else if (p.case === "followUps") next[next.length - 1] = { ...last, followUps: p.value.followUps };
          else if (p.case === "done") next[next.length - 1] = { ...last, grounded: p.value.grounded, streaming: false };
          return next;
        });
      }
      setTurns((prev) => { // 断流未 done=stream 尾兜底
        const next = [...prev]; const last = next[next.length - 1];
        if (last?.streaming) next[next.length - 1] = { ...last, streaming: false };
        return next;
      });
    } catch (e) {
      setTurns((prev) => {
        const next = [...prev]; const last = next[next.length - 1];
        if (last) next[next.length - 1] = { ...last, answer: last.answer + `\n\n[错误] ${String(e)}`, streaming: false };
        return next;
      });
    } finally {
      setBusy(false);
    }
  }, [busy, turns, envOff, page]);

  const note = useCallback(async (i: number) => {
    const t = turns[i];
    if (!t) return;
    setNoting(i);
    try {
      await client.appendAnalystNote({ question: t.question, summary: t.answer, citations: t.citations });
      setTurns((prev) => prev.map((x, j) => (j === i ? { ...x, noted: true } : x)));
    } finally {
      setNoting(null);
    }
  }, [turns]);

  if (!open) return null;
  return (
    <div className="analyst-drawer">
      <div className="analyst-head">
        <b>分析员</b>
        <span className="muted" style={{ fontSize: 12 }}>
          {envOff ? "环境关" : `环境：${PAGE_LABEL[page] ?? page}`}
          <a href="#" style={{ marginLeft: 6 }} onClick={(e) => { e.preventDefault(); setEnvOff(!envOff); }}>{envOff ? "开" : "关"}</a>
        </span>
        <button className="analyst-close" onClick={onClose}>✕</button>
      </div>
      <div className="analyst-body" ref={bodyRef}>
        {turns.length === 0 && <div className="muted" style={{ fontSize: 12 }}>问点什么——答文带引证可「记一下→待归纳」。</div>}
        {turns.map((t, i) => (
          <div key={i} className="analyst-turn">
            <div className="analyst-q">{t.question}</div>
            <div className="analyst-a">
              {t.answer || (t.streaming ? "…" : "")}
              {t.citations.length > 0 && (
                <div className="analyst-cites">
                  {t.citations.map((c, j) => <span key={j} className="analyst-cite" title={c.excerpt}>[{c.kind}:{c.ref}]</span>)}
                </div>
              )}
              {!t.streaming && !t.grounded && t.answer ? <div className="warn" style={{ fontSize: 12 }}>零引用答——仅供参考</div> : null}
              {!t.streaming && t.answer && t.grounded ? (
                <button type="button" className="analyst-note-btn" disabled={t.noted || noting === i} onClick={() => void note(i)}>
                  {t.noted ? "已记" : noting === i ? "记录中…" : "记一下→待归纳"}
                </button>
              ) : null}
            </div>
            {t.followUps.length > 0 && (
              <div className="analyst-followups">
                {t.followUps.map((f, j) => (
                  <button key={j} type="button" className="analyst-followup" disabled={busy} onClick={() => void ask(f)}>{f}</button>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
      <div className="analyst-input">
        <textarea value={input} onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); void ask(input); } }}
          placeholder="问…（Enter 发送，Shift+Enter 换行）" rows={2} />
        <button disabled={busy || !input.trim()} onClick={() => void ask(input)}>{busy ? "…" : "发送"}</button>
      </div>
    </div>
  );
}
