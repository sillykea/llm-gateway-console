interface Props {
  label: string;
  value: string;
  hint?: string;
  tone?: "default" | "warn" | "bad";
}

export function Stat({ label, value, hint, tone = "default" }: Props) {
  return (
    <div className={`stat stat-${tone}`}>
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {hint && <div className="stat-hint">{hint}</div>}
    </div>
  );
}
