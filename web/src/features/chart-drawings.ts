// Chart drawing tools rendered as a lightweight-charts series primitive.
//
// Anchors are stored as (unix seconds, price) so drawings survive interval
// changes and reloads. Times are mapped to fractional logical indices against
// the loaded bars, extrapolating by the interval before the first and after
// the last bar so drawings can extend into the future.

import type {
  IChartApi, IPrimitivePaneRenderer, IPrimitivePaneView, ISeriesApi, ISeriesPrimitive, ISeriesPrimitiveAxisView,
  Logical, PrimitiveHoveredItem, SeriesAttachedParameter, Time,
} from "lightweight-charts";

export type Tool = "cursor" | "trend" | "ray" | "hline" | "vline" | "rect" | "fib" | "measure";
export type DrawingKind = Exclude<Tool, "cursor">;

export interface Anchor {
  time: number;
  price: number;
}

export interface Drawing {
  id: string;
  kind: DrawingKind;
  points: Anchor[];
}

export interface Bar {
  time: number;
  open: number;
  high: number;
  low: number;
  close: number;
}

/** Points needed to complete each kind. */
export const POINTS: Record<DrawingKind, number> = { trend: 2, ray: 2, hline: 1, vline: 1, rect: 2, fib: 2, measure: 2 };

export const FIB_LEVELS = [0, 0.236, 0.382, 0.5, 0.618, 0.786, 1];

const LINE = "#d2dad7";
const ACCENT = "#50d2c1";
const DOWN = "#ed7088";
const PANEL = "#0f1a1f";
const HIT = 6;

// ---------------------------------------------------------------------------
// Time <-> logical index mapping (pure, tested).

export function timeToLogical(times: readonly number[], interval: number, time: number): number | null {
  const n = times.length;
  if (n === 0) return null;
  const first = times[0]!;
  const last = times[n - 1]!;
  if (time <= first) return (time - first) / interval;
  if (time >= last) return n - 1 + (time - last) / interval;
  let lo = 0;
  let hi = n - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (times[mid]! <= time) lo = mid;
    else hi = mid;
  }
  const a = times[lo]!;
  const b = times[hi]!;
  return lo + (time - a) / (b - a);
}

/** Time of a (rounded) logical index; outside the data it is extrapolated. */
export function logicalToTime(times: readonly number[], interval: number, logical: number): number | null {
  const n = times.length;
  if (n === 0) return null;
  const index = Math.round(logical);
  if (index < 0) return times[0]! + index * interval;
  if (index > n - 1) return times[n - 1]! + (index - (n - 1)) * interval;
  return times[index]!;
}

export function distanceToSegment(px: number, py: number, ax: number, ay: number, bx: number, by: number): number {
  const dx = bx - ax;
  const dy = by - ay;
  const length = dx * dx + dy * dy;
  const t = length === 0 ? 0 : Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / length));
  return Math.hypot(px - (ax + t * dx), py - (ay + t * dy));
}

/** Extends the segment a→b past b to the left/right edge of a pane `width` wide. */
export function rayEnd(ax: number, ay: number, bx: number, by: number, width: number): [number, number] {
  const dx = bx - ax;
  const dy = by - ay;
  if (dx === 0 && dy === 0) return [bx, by];
  if (dx === 0) return [bx, dy > 0 ? 1e5 : -1e5];
  const edge = dx > 0 ? width : 0;
  return [edge, ay + ((edge - ax) / dx) * dy];
}

export function duration(seconds: number): string {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days) return hours ? `${days}d ${hours}h` : `${days}d`;
  if (hours) return minutes ? `${hours}h ${minutes}m` : `${hours}h`;
  return `${minutes}m`;
}

export function intervalSeconds(interval: string): number {
  const match = /^(\d+)([mhdw])$/.exec(interval);
  if (!match) return 60;
  const unit = { m: 60, h: 3600, d: 86400, w: 604800 }[match[2] as "m" | "h" | "d" | "w"];
  return Number(match[1]) * unit;
}

// ---------------------------------------------------------------------------

interface Screen {
  x: number;
  y: number;
}

export interface Hit {
  id: string;
  /** Index of the grabbed anchor, or null when the body was hit. */
  handle: number | null;
}

let sequence = 0;
const newId = () => Date.now().toString(36) + (sequence++).toString(36);

/**
 * Holds the drawings of one chart and paints them. The React component owns
 * tool state and pointer handling; this class owns geometry and painting.
 */
export class DrawingLayer implements ISeriesPrimitive<Time> {
  private chart: IChartApi | null = null;
  private series: ISeriesApi<"Candlestick"> | null = null;
  private requestUpdate: () => void = () => {};
  private bars: Bar[] = [];
  private times: number[] = [];
  private interval = 60;
  private storageKey = "";

  drawings: Drawing[] = [];
  /** Drawing in progress (not yet committed). */
  pending: Drawing | null = null;
  /** Transient measurement; never persisted. */
  measure: Drawing | null = null;
  selected: string | null = null;
  hovered: string | null = null;
  hidden = false;

  private readonly views: readonly IPrimitivePaneView[];
  private axisViews: ISeriesPrimitiveAxisView[] = [];

  constructor() {
    const renderer: IPrimitivePaneRenderer = {
      draw: (target) => target.useMediaCoordinateSpace(({ context, mediaSize }) => this.paint(context, mediaSize.width, mediaSize.height)),
    };
    this.views = [{ zOrder: () => "top", renderer: () => renderer }];
  }

  // -- lightweight-charts primitive ------------------------------------------

  attached(param: SeriesAttachedParameter<Time>) {
    this.chart = param.chart as IChartApi;
    this.series = param.series as ISeriesApi<"Candlestick">;
    this.requestUpdate = param.requestUpdate;
  }

  detached() {
    this.chart = null;
    this.series = null;
    this.requestUpdate = () => {};
  }

  paneViews() {
    return this.views;
  }

  updateAllViews() {
    const next: ISeriesPrimitiveAxisView[] = [];
    if (!this.hidden && this.series) {
      const all = this.pending ? [...this.drawings, this.pending] : this.drawings;
      for (const drawing of all) {
        if (drawing.kind !== "hline") continue;
        const price = drawing.points[0]!.price;
        const y = this.series.priceToCoordinate(price);
        if (y === null) continue;
        const text = this.series.priceFormatter().format(price);
        const back = drawing.id === this.selected ? ACCENT : LINE;
        next.push({ coordinate: () => y, text: () => text, textColor: () => PANEL, backColor: () => back });
      }
    }
    this.axisViews = next;
  }

  priceAxisViews() {
    return this.axisViews;
  }

  hitTest(x: number, y: number): PrimitiveHoveredItem | null {
    const hit = this.hit(x, y);
    if (!hit) return null;
    return { externalId: hit.id, zOrder: "top", cursorStyle: hit.handle === null ? "move" : "pointer" };
  }

  // -- data --------------------------------------------------------------------

  setInterval(seconds: number) {
    this.interval = seconds;
  }

  setBars(bars: Bar[]) {
    this.bars = bars;
    this.times = bars.map((bar) => bar.time);
    this.update();
  }

  updateBar(bar: Bar) {
    const last = this.bars.at(-1);
    if (last && bar.time < last.time) return;
    if (last && bar.time === last.time) this.bars[this.bars.length - 1] = bar;
    else {
      this.bars.push(bar);
      this.times.push(bar.time);
    }
  }

  /** Switches to the drawings stored under `key` (one set per instrument). */
  load(key: string) {
    this.storageKey = key;
    this.pending = null;
    this.measure = null;
    this.selected = null;
    this.hovered = null;
    try {
      const raw = localStorage.getItem(key);
      const parsed: unknown = raw ? JSON.parse(raw) : [];
      this.drawings = Array.isArray(parsed) ? parsed.filter(valid) : [];
    } catch {
      this.drawings = [];
    }
    this.update();
  }

  private save() {
    if (!this.storageKey) return;
    try {
      if (this.drawings.length) localStorage.setItem(this.storageKey, JSON.stringify(this.drawings));
      else localStorage.removeItem(this.storageKey);
    } catch {
      // Storage full or unavailable: drawings stay for this session.
    }
  }

  update() {
    this.updateAllViews();
    this.requestUpdate();
  }

  // -- editing -----------------------------------------------------------------

  begin(kind: DrawingKind, at: Anchor) {
    const drawing: Drawing = { id: newId(), kind, points: Array.from({ length: POINTS[kind] }, () => ({ ...at })) };
    if (kind === "measure") {
      this.measure = drawing;
      this.pending = null;
    } else {
      this.pending = drawing;
    }
    this.update();
  }

  /** Moves the free (last) point of the drawing in progress. */
  stretch(at: Anchor) {
    const target = this.pending ?? this.measure;
    if (!target) return;
    target.points[target.points.length - 1] = at;
    this.update();
  }

  /** Commits the drawing in progress and selects it. */
  commit(): string | null {
    const drawing = this.pending;
    this.pending = null;
    if (!drawing) {
      this.update();
      return null;
    }
    this.drawings = [...this.drawings, drawing];
    this.selected = drawing.id;
    this.save();
    this.update();
    return drawing.id;
  }

  cancel() {
    this.pending = null;
    this.measure = null;
    this.update();
  }

  select(id: string | null) {
    if (this.selected === id) return;
    this.selected = id;
    this.update();
  }

  setHovered(id: string | null) {
    if (this.hovered === id) return;
    this.hovered = id;
    this.update();
  }

  remove(id: string) {
    this.drawings = this.drawings.filter((drawing) => drawing.id !== id);
    if (this.selected === id) this.selected = null;
    this.save();
    this.update();
  }

  removeAll() {
    this.drawings = [];
    this.selected = null;
    this.measure = null;
    this.pending = null;
    this.save();
    this.update();
  }

  setHidden(hidden: boolean) {
    this.hidden = hidden;
    if (hidden) {
      this.selected = null;
      this.pending = null;
      this.measure = null;
    }
    this.update();
  }

  /** Replaces the anchors of a committed drawing while dragging; persist with `finishEdit`. */
  setPoints(id: string, points: Anchor[]) {
    this.drawings = this.drawings.map((drawing) => (drawing.id === id ? { ...drawing, points } : drawing));
    this.update();
  }

  finishEdit() {
    this.save();
  }

  get(id: string): Drawing | undefined {
    return this.drawings.find((drawing) => drawing.id === id);
  }

  // -- coordinates -------------------------------------------------------------

  private axis(): { x0: number; spacing: number } | null {
    const timeScale = this.chart?.timeScale();
    if (!timeScale) return null;
    const x0 = timeScale.logicalToCoordinate(0 as Logical);
    const x1 = timeScale.logicalToCoordinate(1 as Logical);
    if (x0 === null || x1 === null || x1 === x0) return null;
    return { x0, spacing: x1 - x0 };
  }

  logicalOf(time: number): number | null {
    return timeToLogical(this.times, this.interval, time);
  }

  private toScreen(anchor: Anchor): Screen | null {
    const axis = this.axis();
    const logical = this.logicalOf(anchor.time);
    const y = this.series?.priceToCoordinate(anchor.price);
    if (!axis || logical === null || y === null || y === undefined) return null;
    return { x: axis.x0 + logical * axis.spacing, y };
  }

  /** Pane point → anchor on the nearest bar; with `magnet`, the price snaps to that bar's OHLC. */
  fromScreen(x: number, y: number, magnet: boolean): Anchor | null {
    const axis = this.axis();
    const price = this.series?.coordinateToPrice(y);
    if (!axis || price === null || price === undefined) return null;
    const logical = Math.round((x - axis.x0) / axis.spacing);
    const time = logicalToTime(this.times, this.interval, logical);
    if (time === null) return null;
    let value = price as number;
    const bar = this.bars[logical];
    if (magnet && bar && this.series) {
      let best = Infinity;
      for (const candidate of [bar.open, bar.high, bar.low, bar.close]) {
        const cy = this.series.priceToCoordinate(candidate);
        if (cy === null) continue;
        if (Math.abs(cy - y) < best) {
          best = Math.abs(cy - y);
          value = candidate;
        }
      }
    }
    return { time, price: value };
  }

  /** Moves anchors by whole bars and a price delta. */
  shift(points: Anchor[], deltaBars: number, deltaPrice: number): Anchor[] {
    return points.map((point) => {
      const logical = this.logicalOf(point.time);
      const time = logical === null ? null : logicalToTime(this.times, this.interval, logical + deltaBars);
      return { time: time ?? point.time, price: point.price + deltaPrice };
    });
  }

  // -- hit testing -------------------------------------------------------------

  hit(x: number, y: number): Hit | null {
    if (this.hidden || !this.chart) return null;
    const width = this.chart.timeScale().width();
    const grab = (drawing: Drawing) => this.screenPoints(drawing).findIndex((point) => point && Math.hypot(point.x - x, point.y - y) <= HIT);
    // Handles of the selected drawing win over everything else.
    const selected = this.selected ? this.get(this.selected) : undefined;
    if (selected) {
      const handle = grab(selected);
      if (handle >= 0) return { id: selected.id, handle };
    }
    for (let i = this.drawings.length - 1; i >= 0; i--) {
      const drawing = this.drawings[i]!;
      const handle = grab(drawing);
      if (handle >= 0) return { id: drawing.id, handle };
      if (this.near(drawing, x, y, width)) return { id: drawing.id, handle: null };
    }
    return null;
  }

  private screenPoints(drawing: Drawing): Array<Screen | null> {
    return drawing.points.map((point) => this.toScreen(point));
  }

  private near(drawing: Drawing, x: number, y: number, width: number): boolean {
    const points = this.screenPoints(drawing);
    const a = points[0];
    if (!a) return false;
    const b = points[1] ?? a;
    switch (drawing.kind) {
      case "hline":
        return Math.abs(y - a.y) <= HIT;
      case "vline":
        return Math.abs(x - a.x) <= HIT;
      case "trend":
        return distanceToSegment(x, y, a.x, a.y, b.x, b.y) <= HIT;
      case "ray": {
        const [ex, ey] = rayEnd(a.x, a.y, b.x, b.y, width);
        return distanceToSegment(x, y, a.x, a.y, ex, ey) <= HIT;
      }
      case "rect":
        return x >= Math.min(a.x, b.x) - HIT && x <= Math.max(a.x, b.x) + HIT && y >= Math.min(a.y, b.y) - HIT && y <= Math.max(a.y, b.y) + HIT;
      case "fib":
        return x >= Math.min(a.x, b.x) - HIT && FIB_LEVELS.some((level) => Math.abs(y - (b.y + (a.y - b.y) * level)) <= HIT);
      default:
        return false;
    }
  }

  // -- painting ----------------------------------------------------------------

  private paint(ctx: CanvasRenderingContext2D, width: number, height: number) {
    if (this.hidden) return;
    ctx.save();
    ctx.font = "11px 'Inter Variable', system-ui, sans-serif";
    ctx.textBaseline = "middle";
    for (const drawing of this.drawings) this.paintDrawing(ctx, drawing, width, height);
    if (this.pending) this.paintDrawing(ctx, this.pending, width, height);
    if (this.measure) this.paintMeasure(ctx, this.measure);
    ctx.restore();
  }

  private paintDrawing(ctx: CanvasRenderingContext2D, drawing: Drawing, width: number, height: number) {
    const points = this.screenPoints(drawing);
    const a = points[0];
    if (!a) return;
    const b = points[1] ?? a;
    const active = drawing.id === this.selected || drawing === this.pending;
    ctx.strokeStyle = LINE;
    ctx.lineWidth = active || drawing.id === this.hovered ? 2 : 1.5;
    ctx.setLineDash([]);

    switch (drawing.kind) {
      case "trend":
        line(ctx, a.x, a.y, b.x, b.y);
        break;
      case "ray": {
        const [ex, ey] = rayEnd(a.x, a.y, b.x, b.y, width);
        line(ctx, a.x, a.y, ex, ey);
        break;
      }
      case "hline":
        line(ctx, 0, a.y, width, a.y);
        break;
      case "vline":
        line(ctx, a.x, 0, a.x, height);
        break;
      case "rect": {
        const x = Math.min(a.x, b.x);
        const y = Math.min(a.y, b.y);
        ctx.fillStyle = "rgba(210, 218, 215, 0.08)";
        ctx.fillRect(x, y, Math.abs(b.x - a.x), Math.abs(b.y - a.y));
        ctx.strokeRect(x, y, Math.abs(b.x - a.x), Math.abs(b.y - a.y));
        break;
      }
      case "fib":
        this.paintFib(ctx, drawing, a, b, width);
        break;
      default:
        break;
    }

    if (active) for (const point of points) if (point) handle(ctx, point.x, point.y);
  }

  private paintFib(ctx: CanvasRenderingContext2D, drawing: Drawing, a: Screen, b: Screen, width: number) {
    const start = drawing.points[0]!.price;
    const end = drawing.points[1]!.price;
    const left = Math.min(a.x, b.x);
    const format = (price: number) => this.series?.priceFormatter().format(price) ?? price.toFixed(2);
    // Faint band between 0.382 and 0.618.
    const y382 = b.y + (a.y - b.y) * 0.382;
    const y618 = b.y + (a.y - b.y) * 0.618;
    ctx.fillStyle = "rgba(210, 218, 215, 0.06)";
    ctx.fillRect(left, Math.min(y382, y618), width - left, Math.abs(y618 - y382));
    const weight = ctx.lineWidth;
    ctx.lineWidth = 1;
    for (const level of FIB_LEVELS) {
      const y = b.y + (a.y - b.y) * level;
      ctx.strokeStyle = level === 0 || level === 1 ? LINE : "rgba(210, 218, 215, 0.55)";
      line(ctx, left, y, width, y);
      ctx.fillStyle = "#949e9c";
      ctx.fillText(`${level} (${format(end + (start - end) * level)})`, left + 4, y - 7);
    }
    ctx.strokeStyle = "rgba(210, 218, 215, 0.4)";
    ctx.lineWidth = weight;
    ctx.setLineDash([4, 4]);
    line(ctx, a.x, a.y, b.x, b.y);
    ctx.setLineDash([]);
  }

  private paintMeasure(ctx: CanvasRenderingContext2D, drawing: Drawing) {
    const [p, q] = drawing.points as [Anchor, Anchor];
    const a = this.toScreen(p);
    const b = this.toScreen(q);
    if (!a || !b) return;
    const up = q.price >= p.price;
    const color = up ? ACCENT : DOWN;
    const left = Math.min(a.x, b.x);
    const top = Math.min(a.y, b.y);
    const w = Math.abs(b.x - a.x);
    const h = Math.abs(b.y - a.y);
    ctx.fillStyle = up ? "rgba(80, 210, 193, 0.14)" : "rgba(237, 112, 136, 0.14)";
    ctx.fillRect(left, top, w, h);
    ctx.strokeStyle = color;
    ctx.lineWidth = 1;
    arrow(ctx, a.x + (b.x - a.x) / 2, a.y, a.x + (b.x - a.x) / 2, b.y);
    arrow(ctx, a.x, a.y + (b.y - a.y) / 2, b.x, a.y + (b.y - a.y) / 2);

    const change = q.price - p.price;
    const percent = p.price ? (change / p.price) * 100 : 0;
    const bars = Math.round((this.logicalOf(q.time) ?? 0) - (this.logicalOf(p.time) ?? 0));
    const format = (value: number) => this.series?.priceFormatter().format(value) ?? value.toFixed(2);
    const sign = change > 0 ? "+" : change < 0 ? "-" : "";
    const lines = [`${sign}${format(Math.abs(change))} (${sign}${Math.abs(percent).toFixed(2)}%)`, `${bars} bars, ${duration(Math.abs(q.time - p.time))}`];
    const boxWidth = Math.max(...lines.map((text) => ctx.measureText(text).width)) + 16;
    const boxHeight = 36;
    const bx = left + w / 2 - boxWidth / 2;
    const by = up ? top - boxHeight - 6 : top + h + 6;
    ctx.fillStyle = color;
    roundRect(ctx, bx, by, boxWidth, boxHeight, 4);
    ctx.fill();
    ctx.fillStyle = PANEL;
    ctx.textAlign = "center";
    ctx.fillText(lines[0]!, bx + boxWidth / 2, by + 11);
    ctx.fillText(lines[1]!, bx + boxWidth / 2, by + 25);
    ctx.textAlign = "start";
  }
}

function valid(value: unknown): value is Drawing {
  const drawing = value as Drawing;
  return !!drawing && typeof drawing.id === "string" && drawing.kind in POINTS && drawing.kind !== "measure"
    && Array.isArray(drawing.points) && drawing.points.length === POINTS[drawing.kind]
    && drawing.points.every((point) => Number.isFinite(point?.time) && Number.isFinite(point?.price));
}

function line(ctx: CanvasRenderingContext2D, ax: number, ay: number, bx: number, by: number) {
  ctx.beginPath();
  ctx.moveTo(ax, ay);
  ctx.lineTo(bx, by);
  ctx.stroke();
}

function handle(ctx: CanvasRenderingContext2D, x: number, y: number) {
  ctx.beginPath();
  ctx.arc(x, y, 4, 0, Math.PI * 2);
  ctx.fillStyle = PANEL;
  ctx.fill();
  ctx.lineWidth = 1.5;
  ctx.strokeStyle = ACCENT;
  ctx.stroke();
}

function arrow(ctx: CanvasRenderingContext2D, ax: number, ay: number, bx: number, by: number) {
  line(ctx, ax, ay, bx, by);
  if (Math.hypot(bx - ax, by - ay) < 8) return;
  const angle = Math.atan2(by - ay, bx - ax);
  ctx.beginPath();
  ctx.moveTo(bx, by);
  ctx.lineTo(bx - 6 * Math.cos(angle - 0.5), by - 6 * Math.sin(angle - 0.5));
  ctx.moveTo(bx, by);
  ctx.lineTo(bx - 6 * Math.cos(angle + 0.5), by - 6 * Math.sin(angle + 0.5));
  ctx.stroke();
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r);
  ctx.arcTo(x, y, x + w, y, r);
  ctx.closePath();
}
