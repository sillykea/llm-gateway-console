const usd = new Intl.NumberFormat("en-NZ", { style: "currency", currency: "USD", currencyDisplay: "narrowSymbol" });
const compact = new Intl.NumberFormat("en-NZ", { notation: "compact", maximumFractionDigits: 1 });
const pct = new Intl.NumberFormat("en-NZ", { style: "percent", maximumFractionDigits: 1 });
const dateTime = new Intl.DateTimeFormat("en-NZ", { dateStyle: "medium", timeStyle: "short" });
const dateOnly = new Intl.DateTimeFormat("en-NZ", { day: "numeric", month: "short" });
const hourOnly = new Intl.DateTimeFormat("en-NZ", { hour: "numeric" });

export const fmt = {
  usd: (n: number) => usd.format(n),
  count: (n: number) => compact.format(Math.round(n)),
  pct: (n: number) => pct.format(n),
  seconds: (s: number) => (s < 1 ? `${Math.round(s * 1000)} ms` : `${s.toFixed(2)} s`),
  dateTime: (iso: string) => dateTime.format(new Date(iso)),
  day: (iso: string) => dateOnly.format(new Date(iso)),
  hour: (iso: string) => hourOnly.format(new Date(iso)),
  limit: (n: number, unit: string) => (n === 0 ? "No limit" : `${n.toLocaleString("en-NZ")} ${unit}`),
};
