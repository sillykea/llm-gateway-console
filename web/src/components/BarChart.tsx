import { useId, useState } from "react";
import type { Point } from "../api";

interface Props {
  title: string;
  points: Point[];
  label: (iso: string) => string;
  value: (n: number) => string;
  /** Show every nth x label so they don't overlap. */
  labelEvery?: number;
}

// A small, dependency-free SVG bar chart. Bars are focusable so keyboard
// users get the same tooltip as mouse users.
export function BarChart({ title, points, label, value, labelEvery = 1 }: Props) {
  const [active, setActive] = useState<number | null>(null);
  const titleId = useId();
  const width = 640;
  const height = 180;
  const pad = { top: 12, right: 8, bottom: 24, left: 8 };
  const innerW = width - pad.left - pad.right;
  const innerH = height - pad.top - pad.bottom;
  const max = Math.max(...points.map((p) => p.v), 0);
  const slot = points.length > 0 ? innerW / points.length : innerW;
  const barW = Math.max(2, slot * 0.7);
  const shown = active !== null ? points[active] : undefined;

  return (
    <figure className="chart">
      <figcaption id={titleId}>
        <span>{title}</span>
        <span className="chart-readout" aria-live="polite">
          {shown ? `${label(shown.t)}: ${value(shown.v)}` : ""}
        </span>
      </figcaption>
      {points.length === 0 ? (
        <p className="empty">No data yet.</p>
      ) : (
        <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-labelledby={titleId} preserveAspectRatio="none">
          <line className="axis" x1={pad.left} x2={width - pad.right} y1={pad.top + innerH} y2={pad.top + innerH} />
          {points.map((p, i) => {
            const h = max > 0 ? (p.v / max) * innerH : 0;
            const x = pad.left + i * slot + (slot - barW) / 2;
            return (
              <g key={p.t}>
                <rect
                  className={i === active ? "bar active" : "bar"}
                  x={x}
                  y={pad.top + innerH - h}
                  width={barW}
                  height={Math.max(h, p.v > 0 ? 1 : 0)}
                  rx={2}
                  tabIndex={0}
                  aria-label={`${label(p.t)}: ${value(p.v)}`}
                  onMouseEnter={() => setActive(i)}
                  onMouseLeave={() => setActive(null)}
                  onFocus={() => setActive(i)}
                  onBlur={() => setActive(null)}
                />
                {i % labelEvery === 0 && (
                  <text className="tick" x={x + barW / 2} y={height - 6} textAnchor="middle">
                    {label(p.t)}
                  </text>
                )}
              </g>
            );
          })}
        </svg>
      )}
    </figure>
  );
}
