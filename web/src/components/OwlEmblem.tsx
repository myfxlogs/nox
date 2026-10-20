// OwlEmblem —— 夜枭 emblem（D-638② 定形 A：枭面正形——金环双目=beat_ts
// 枭眼心跳语义；纯 inline SVG 零依赖零新构建件，currentColor 随主题）。
export function OwlEmblem({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-label="夜枭" role="img" style={{ verticalAlign: "-0.25em" }}>
      {/* 脸面：颊弧收颏，顶沿中凹（双颊间弧线），耳羽起角 */}
      <path d="M5.5 4.5 L8.5 9 C5.5 14 6.5 20.5 11 24 C14 26.5 18 26.5 21 24 C25.5 20.5 26.5 14 23.5 9 L26.5 4.5 C23 7.5 19.5 7.2 16 5.2 C12.5 7.2 9 7.5 5.5 4.5 Z"
        fill="currentColor" opacity="0.92" />
      {/* 金环双目（大环=瞳域） */}
      <circle cx="11.2" cy="14.5" r="4.6" fill="#d9a441" />
      <circle cx="20.8" cy="14.5" r="4.6" fill="#d9a441" />
      <circle cx="11.2" cy="14.5" r="2.2" fill="#10141c" />
      <circle cx="20.8" cy="14.5" r="2.2" fill="#10141c" />
      {/* 高光 */}
      <circle cx="12.1" cy="13.6" r="0.8" fill="#e8eefc" />
      <circle cx="21.7" cy="13.6" r="0.8" fill="#e8eefc" />
      {/* 菱喙 */}
      <path d="M16 20.5 L14.6 22.3 L16 24 L17.4 22.3 Z" fill="#d9a441" />
    </svg>
  );
}
