// NoxMark —— NOX 字标（D-638② 定形 C：名与神合一——字母 O 即枭面，
// 双目=beat_ts 枭眼心跳语义落在品牌词里；纯 inline SVG 零依赖）。
// currentColor 继承文字色；金=枭眼/喙唯一点缀色。
export function NoxMark({ height = 34 }: { height?: number }) {
  const w = (height * 120) / 40;
  return (
    <svg width={w} height={height} viewBox="0 0 120 40" role="img" aria-label="NOX" style={{ verticalAlign: "middle" }}>
      {/* N —— 圆头粗笔画 */}
      <path d="M14 33 V9 L30 33 V9" fill="none" stroke="currentColor" strokeWidth="6.5" strokeLinecap="round" strokeLinejoin="round" />
      {/* O = 枭面：耳羽入字形 */}
      <path d="M47 10 L51 3.5 L54 8.5 M66 8.5 L69 3.5 L73 10" fill="none" stroke="currentColor" strokeWidth="4" strokeLinecap="round" strokeLinejoin="round" />
      <circle cx="60" cy="21" r="12" fill="none" stroke="currentColor" strokeWidth="6.5" />
      {/* 枭目（金）+小喙 */}
      <circle cx="55.5" cy="19" r="2.1" fill="#d9a441" />
      <circle cx="64.5" cy="19" r="2.1" fill="#d9a441" />
      <path d="M60 23.4 L58.4 25.4 L61.6 25.4 Z" fill="#d9a441" />
      {/* X */}
      <path d="M92 9 L112 33 M112 9 L92 33" fill="none" stroke="currentColor" strokeWidth="6.5" strokeLinecap="round" />
    </svg>
  );
}
