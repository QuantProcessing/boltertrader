// Display formatting. Values arrive as decimal strings; arithmetic for display
// uses Number, which is exact enough for rendering but never for order sizes —
// sizes are snapped with snapToStep, which works on strings.

export function decimals(step: string): number {
  const dot = step.indexOf(".");
  if (dot < 0) return 0;
  return step.slice(dot + 1).replace(/0+$/, "").length;
}

const cache = new Map<string, Intl.NumberFormat>();
function formatter(min: number, max: number): Intl.NumberFormat {
  const key = min + ":" + max;
  let value = cache.get(key);
  if (!value) {
    value = new Intl.NumberFormat("en-US", { minimumFractionDigits: min, maximumFractionDigits: max });
    cache.set(key, value);
  }
  return value;
}

export function num(value: string | number | undefined | null, digits?: number): string {
  if (value === undefined || value === null || value === "") return "—";
  const n = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(n)) return "—";
  if (digits !== undefined) return formatter(digits, digits).format(n);
  const abs = Math.abs(n);
  const max = abs >= 1000 ? 2 : abs >= 1 ? 4 : abs >= 0.01 ? 6 : 8;
  return formatter(0, max).format(n);
}

export function price(value: string | number | undefined, tickSize?: string): string {
  return num(value, tickSize ? decimals(tickSize) : undefined);
}

export function usd(value: string | number | undefined, digits = 2): string {
  if (value === undefined || value === "") return "—";
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  return (n < 0 ? "-$" : "$") + formatter(digits, digits).format(Math.abs(n));
}

export function signed(value: number, digits = 2): string {
  return (value > 0 ? "+" : value < 0 ? "-" : "") + formatter(digits, digits).format(Math.abs(value));
}

export function pct(value: number, digits = 2): string {
  return signed(value * 100, digits) + "%";
}

export function compact(value: string | number | undefined): string {
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  return new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 2 }).format(n);
}

export function time(ms: number): string {
  if (!ms) return "—";
  const d = new Date(ms);
  return d.toLocaleString("en-GB", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

export function countdown(targetMs: number | undefined, now: number): string {
  if (!targetMs) return "—";
  const total = Math.max(0, Math.floor((targetMs - now) / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return [h, m, s].map((v) => String(v).padStart(2, "0")).join(":");
}

/**
 * Floors a positive decimal string to a multiple of step, as a string, using
 * scaled BigInt arithmetic so no binary rounding error can reach an order.
 */
export function snapToStep(value: string, step: string): string {
  const scale = Math.max(decimals(step), decimals(value));
  const toUnits = (text: string) => {
    const [whole = "0", fraction = ""] = text.split(".");
    return BigInt(whole + fraction.padEnd(scale, "0").slice(0, scale));
  };
  if (!/^\d*\.?\d*$/.test(value) || value === "" || value === ".") return "";
  const units = toUnits(value);
  const stepUnits = toUnits(step);
  if (stepUnits <= 0n) return value;
  const snapped = (units / stepUnits) * stepUnits;
  const text = snapped.toString().padStart(scale + 1, "0");
  const whole = text.slice(0, text.length - scale) || "0";
  const fraction = scale ? text.slice(text.length - scale).replace(/0+$/, "") : "";
  return fraction ? `${whole}.${fraction}` : whole;
}

const VENUE_LABEL: Record<string, string> = {
  binance: "Binance", okx: "OKX", bybit: "Bybit", bitget: "Bitget", gate: "Gate", hyperliquid: "Hyperliquid", lighter: "Lighter",
};
export const venueLabel = (venue: string) => VENUE_LABEL[venue] ?? venue;

export function instrumentName(id: string): string {
  const parts = id.split(":");
  return parts.slice(2).join(":");
}
