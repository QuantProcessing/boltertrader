import { CandlestickSeries, HistogramSeries, createChart, type IChartApi, type ISeriesApi, type UTCTimestamp } from "lightweight-charts";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

import { api } from "@/api/client";
import type { Candle } from "@/api/types";
import { decimals } from "@/lib/format";
import { useCandleStream, useInstrument } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";
import { DrawingLayer, POINTS, intervalSeconds, type Anchor, type Bar, type Tool } from "./chart-drawings";

const INTERVALS = ["1m", "5m", "15m", "1h", "4h", "1d"] as const;
const UP = "#1fa67d";
const DOWN = "#ed7088";

type Gesture =
  | { kind: "none" }
  /** A drawing waits for its last point: a second click, or release after a drag. */
  | { kind: "placing"; x: number; y: number; dragged: boolean }
  /** A committed drawing is being moved (handle === null) or reshaped. */
  | { kind: "drag"; id: string; handle: number | null; start: Anchor; points: Anchor[] };

export function ChartPanel() {
  const { instrumentId } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const [interval, setIntervalValue] = useState<string>(() => localStorage.getItem("bolter.interval") ?? "15m");
  const host = useRef<HTMLDivElement>(null);
  const chart = useRef<IChartApi | null>(null);
  const candles = useRef<ISeriesApi<"Candlestick"> | null>(null);
  const volume = useRef<ISeriesApi<"Histogram"> | null>(null);
  const layer = useRef(new DrawingLayer());
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [failure, setFailure] = useState("");
  const [attempt, setAttempt] = useState(0);

  const [tool, setToolState] = useState<Tool>("cursor");
  const [magnet, setMagnet] = useState(() => localStorage.getItem("bolter.chart.magnet") === "1");
  const [hidden, setHidden] = useState(false);
  const [count, setCount] = useState(0);
  const toolRef = useRef(tool);
  const magnetRef = useRef(magnet);
  const gesture = useRef<Gesture>({ kind: "none" });
  magnetRef.current = magnet;

  const setTool = useCallback((next: Tool) => {
    toolRef.current = next;
    setToolState(next);
  }, []);
  const refreshCount = useCallback(() => setCount(layer.current.drawings.length), []);

  useEffect(() => {
    if (!host.current) return;
    const instance = createChart(host.current, {
      autoSize: true,
      layout: { background: { color: "#0f1a1f" }, textColor: "#949e9c", fontSize: 11, fontFamily: "Inter Variable, system-ui", attributionLogo: false },
      grid: { vertLines: { color: "#1c262c" }, horzLines: { color: "#1c262c" } },
      rightPriceScale: { borderColor: "#273035" },
      timeScale: { borderColor: "#273035", timeVisible: true, secondsVisible: false },
      crosshair: { mode: 0 },
    });
    candles.current = instance.addSeries(CandlestickSeries, {
      upColor: UP, downColor: DOWN, borderUpColor: UP, borderDownColor: DOWN, wickUpColor: UP, wickDownColor: DOWN,
    });
    volume.current = instance.addSeries(HistogramSeries, { priceFormat: { type: "volume" }, priceScaleId: "volume" });
    instance.priceScale("volume").applyOptions({ scaleMargins: { top: 0.82, bottom: 0 } });
    candles.current.attachPrimitive(layer.current);
    chart.current = instance;
    return () => {
      instance.remove();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    layer.current.load("bolter.drawings." + instrumentId);
    gesture.current = { kind: "none" };
    refreshCount();
  }, [instrumentId, refreshCount]);

  useEffect(() => {
    if (!candles.current || !instrument) return;
    const precision = decimals(instrument.tickSize);
    candles.current.applyOptions({ priceFormat: { type: "price", precision, minMove: Number(instrument.tickSize) } });
  }, [instrument]);

  useEffect(() => {
    const controller = new AbortController();
    setStatus("loading");
    setFailure("");
    candles.current?.setData([]);
    volume.current?.setData([]);
    layer.current.setInterval(intervalSeconds(interval));
    layer.current.setBars([]);
    api.candles(instrumentId, interval, undefined, controller.signal).then((rows) => {
      const bars = rows.map(toBar);
      candles.current?.setData(bars);
      volume.current?.setData(rows.map(toVolume));
      layer.current.setBars(bars.map(plain));
      chart.current?.timeScale().scrollToRealTime();
      setStatus("ready");
    }).catch((error) => {
      if (!controller.signal.aborted) {
        console.warn("candles", error);
        setFailure(error instanceof Error ? error.message : String(error));
        setStatus("error");
      }
    });
    return () => controller.abort();
  }, [instrumentId, interval, attempt]);

  const onCandle = useCallback((candle: Candle) => {
    try {
      const bar = toBar(candle);
      candles.current?.update(bar);
      volume.current?.update(toVolume(candle));
      layer.current.updateBar(plain(bar));
    } catch {
      // An out-of-order update (older than the last bar) is ignored.
    }
  }, []);
  useCandleStream(instrumentId, interval, onCandle);

  // Pointer handling for drawing tools. Mouse-down is intercepted in the
  // capture phase so the chart does not pan while a drawing is placed or dragged.
  useEffect(() => {
    const element = host.current;
    if (!element) return;
    const drawings = layer.current;
    const local = (event: MouseEvent) => {
      const rect = element.getBoundingClientRect();
      return { x: event.clientX - rect.left, y: event.clientY - rect.top };
    };
    const inPane = (x: number, y: number) => {
      const instance = chart.current;
      if (!instance) return false;
      return x >= 0 && y >= 0 && x <= instance.timeScale().width() && y <= instance.paneSize().height;
    };
    const finish = () => {
      gesture.current = { kind: "none" };
      if (toolRef.current !== "measure") drawings.commit();
      refreshCount();
      setTool("cursor");
    };

    const onDown = (event: MouseEvent) => {
      if (event.button !== 0) return;
      const { x, y } = local(event);
      if (!inPane(x, y)) return;
      const current = toolRef.current;
      if (current !== "measure" && gesture.current.kind === "none" && drawings.measure) drawings.cancel();

      if (current !== "cursor") {
        event.stopPropagation();
        event.preventDefault();
        const anchor = drawings.fromScreen(x, y, magnetRef.current);
        if (!anchor) return;
        if (gesture.current.kind === "placing") {
          drawings.stretch(anchor);
          finish();
          return;
        }
        drawings.begin(current, anchor);
        if (POINTS[current] === 1) finish();
        else gesture.current = { kind: "placing", x, y, dragged: false };
        return;
      }

      const hit = drawings.hit(x, y);
      if (!hit) {
        drawings.select(null);
        return;
      }
      event.stopPropagation();
      event.preventDefault();
      drawings.select(hit.id);
      const start = drawings.fromScreen(x, y, false);
      const drawing = drawings.get(hit.id);
      if (start && drawing) gesture.current = { kind: "drag", id: hit.id, handle: hit.handle, start, points: drawing.points };
    };

    const onMove = (event: MouseEvent) => {
      const { x, y } = local(event);
      const current = gesture.current;
      if (current.kind === "placing") {
        const anchor = drawings.fromScreen(x, y, magnetRef.current);
        if (anchor) drawings.stretch(anchor);
        if ((event.buttons & 1) && Math.hypot(x - current.x, y - current.y) > 4) current.dragged = true;
        return;
      }
      if (current.kind === "drag") {
        const anchor = drawings.fromScreen(x, y, magnetRef.current && current.handle !== null);
        if (!anchor) return;
        if (current.handle !== null) {
          const points = current.points.slice();
          points[current.handle] = anchor;
          drawings.setPoints(current.id, points);
        } else {
          const from = drawings.logicalOf(current.start.time) ?? 0;
          const to = drawings.logicalOf(anchor.time) ?? 0;
          drawings.setPoints(current.id, drawings.shift(current.points, Math.round(to - from), anchor.price - current.start.price));
        }
        return;
      }
      if (toolRef.current === "cursor" && event.target instanceof Node && element.contains(event.target)) {
        drawings.setHovered(inPane(x, y) ? drawings.hit(x, y)?.id ?? null : null);
      }
    };

    const onUp = () => {
      const current = gesture.current;
      if (current.kind === "placing" && current.dragged) finish();
      else if (current.kind === "drag") {
        drawings.finishEdit();
        gesture.current = { kind: "none" };
      }
    };

    const onKey = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.tagName === "SELECT" || target.isContentEditable)) return;
      if (event.key === "Escape") {
        if (gesture.current.kind !== "none" || toolRef.current !== "cursor" || drawings.measure) {
          gesture.current = { kind: "none" };
          drawings.cancel();
          setTool("cursor");
        } else drawings.select(null);
      } else if ((event.key === "Delete" || event.key === "Backspace") && drawings.selected && gesture.current.kind === "none") {
        event.preventDefault();
        drawings.remove(drawings.selected);
        refreshCount();
      }
    };

    element.addEventListener("mousedown", onDown, true);
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
    window.addEventListener("keydown", onKey);
    return () => {
      element.removeEventListener("mousedown", onDown, true);
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
      window.removeEventListener("keydown", onKey);
    };
  }, [refreshCount, setTool]);

  const changeInterval = (value: string) => {
    gesture.current = { kind: "none" };
    layer.current.cancel();
    setIntervalValue(value);
    localStorage.setItem("bolter.interval", value);
  };

  const pick = (next: Tool) => {
    gesture.current = { kind: "none" };
    layer.current.cancel();
    if (hidden && next !== "cursor") {
      setHidden(false);
      layer.current.setHidden(false);
    }
    setTool(next === tool ? "cursor" : next);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex h-9 shrink-0 items-center gap-1 border-b border-line px-2">
        {INTERVALS.map((value) => (
          <button
            key={value}
            onClick={() => changeInterval(value)}
            className={cx("rounded px-2 py-1", value === interval ? "text-accent" : "text-muted hover:text-text")}
          >
            {value}
          </button>
        ))}
      </div>
      <div className="flex min-h-0 flex-1">
        <div role="toolbar" aria-label="Drawing tools" aria-orientation="vertical" className="flex w-10 shrink-0 flex-col items-center gap-0.5 overflow-y-auto border-r border-line py-1.5">
          {TOOLS.map((item) => (
            <ToolButton key={item.tool} label={item.label} active={tool === item.tool} onClick={() => pick(item.tool)}>
              {item.icon}
            </ToolButton>
          ))}
          <div className="my-1 h-px w-6 shrink-0 bg-line" />
          <ToolButton
            label={magnet ? "Magnet on: snap to open, high, low, close" : "Magnet off"}
            active={magnet}
            onClick={() => {
              localStorage.setItem("bolter.chart.magnet", magnet ? "0" : "1");
              setMagnet(!magnet);
            }}
          >
            <MagnetIcon />
          </ToolButton>
          <ToolButton
            label={hidden ? "Show drawings" : "Hide drawings"}
            active={hidden}
            disabled={!count && !hidden}
            onClick={() => {
              layer.current.setHidden(!hidden);
              setHidden(!hidden);
              setTool("cursor");
            }}
          >
            {hidden ? <EyeOffIcon /> : <EyeIcon />}
          </ToolButton>
          <ToolButton
            label="Remove all drawings (Delete removes the selected one)"
            disabled={!count}
            onClick={() => {
              layer.current.removeAll();
              refreshCount();
            }}
          >
            <TrashIcon />
          </ToolButton>
        </div>
        <div className="relative min-h-0 min-w-0 flex-1">
          <div ref={host} className={cx("absolute inset-0", tool !== "cursor" && "cursor-crosshair")} />
          {status === "loading" && <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center text-muted">Loading chart…</div>}
          {status === "error" && (
            <div role="alert" className="absolute inset-0 z-10 flex items-center justify-center bg-surface/85 p-6">
              <div className="max-w-[360px] text-center">
                <div className="text-md text-text">Chart history unavailable</div>
                {failure && <p className="mt-1.5 line-clamp-4 text-muted">{failure}</p>}
                <button onClick={() => setAttempt((value) => value + 1)} className="mt-3 h-8 rounded-lg border border-line px-4 text-text hover:bg-raised">
                  Retry
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function ToolButton({ label, active, disabled, onClick, children }: {
  label: string;
  active?: boolean;
  disabled?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      aria-pressed={active}
      disabled={disabled}
      onClick={onClick}
      className={cx(
        "flex size-8 shrink-0 items-center justify-center rounded-md transition-colors disabled:cursor-default disabled:opacity-35",
        active ? "text-accent" : "text-text-2 enabled:hover:bg-raised enabled:hover:text-text",
      )}
    >
      {children}
    </button>
  );
}

function toBar(candle: Candle) {
  return { time: Math.floor(candle.t / 1000) as UTCTimestamp, open: Number(candle.o), high: Number(candle.h), low: Number(candle.l), close: Number(candle.c) };
}

function plain(bar: ReturnType<typeof toBar>): Bar {
  return { time: bar.time, open: bar.open, high: bar.high, low: bar.low, close: bar.close };
}

function toVolume(candle: Candle) {
  return { time: Math.floor(candle.t / 1000) as UTCTimestamp, value: Number(candle.v), color: Number(candle.c) >= Number(candle.o) ? "rgba(31,166,125,0.45)" : "rgba(237,112,136,0.45)" };
}

// 20px stroke icons drawn on a 20 grid.
function Icon({ children }: { children: ReactNode }) {
  return (
    <svg viewBox="0 0 20 20" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      {children}
    </svg>
  );
}

const Dot = ({ x, y }: { x: number; y: number }) => <circle cx={x} cy={y} r="1.6" fill="currentColor" stroke="none" />;

const TOOLS: Array<{ tool: Tool; label: string; icon: ReactNode }> = [
  { tool: "cursor", label: "Cross", icon: <Icon><path d="M10 3v5.5M10 11.5V17M3 10h5.5M11.5 10H17" /></Icon> },
  { tool: "trend", label: "Trend line", icon: <Icon><path d="M5 15 15 5" /><Dot x={4.5} y={15.5} /><Dot x={15.5} y={4.5} /></Icon> },
  { tool: "ray", label: "Ray", icon: <Icon><path d="M5 15 18 3" /><Dot x={4.5} y={15.5} /><Dot x={10} y={10.4} /></Icon> },
  { tool: "hline", label: "Horizontal line", icon: <Icon><path d="M2.5 10h15" /><Dot x={10} y={10} /></Icon> },
  { tool: "vline", label: "Vertical line", icon: <Icon><path d="M10 2.5v15" /><Dot x={10} y={10} /></Icon> },
  { tool: "rect", label: "Rectangle", icon: <Icon><rect x="4" y="5.5" width="12" height="9" rx="0.5" /><Dot x={4} y={5.5} /><Dot x={16} y={14.5} /></Icon> },
  { tool: "fib", label: "Fib retracement", icon: <Icon><path d="M3 4.5h14M3 8h14M3 11.5h14M3 15.5h14" /><Dot x={4.5} y={15.5} /><Dot x={15.5} y={4.5} /></Icon> },
  { tool: "measure", label: "Measure", icon: <Icon><path d="m3.5 13.5 10-10 3 3-10 10z" /><path d="m6 11 1.5 1.5M8.5 8.5 10 10M11 6l1.5 1.5" /></Icon> },
];

function MagnetIcon() {
  return <Icon><path d="M5 3.5v6.5a5 5 0 0 0 10 0V3.5h-3.5V10a1.5 1.5 0 0 1-3 0V3.5z" /><path d="M5 6.5h3.5M11.5 6.5H15" /></Icon>;
}

function EyeIcon() {
  return <Icon><path d="M2.5 10s2.8-5 7.5-5 7.5 5 7.5 5-2.8 5-7.5 5-7.5-5-7.5-5z" /><circle cx="10" cy="10" r="2.2" /></Icon>;
}

function EyeOffIcon() {
  return <Icon><path d="M2.5 10s2.8-5 7.5-5 7.5 5 7.5 5-2.8 5-7.5 5-7.5-5-7.5-5z" /><circle cx="10" cy="10" r="2.2" /><path d="M3.5 16.5 16.5 3.5" /></Icon>;
}

function TrashIcon() {
  return <Icon><path d="M4 5.5h12M8 5.5V4h4v1.5M5.5 5.5l.7 10.5h7.6l.7-10.5M8.5 8.5v5M11.5 8.5v5" /></Icon>;
}
