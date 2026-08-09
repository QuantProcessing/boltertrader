// Playwright bundles pngjs for its screenshot assertions without publishing this internal module's types.
// @ts-expect-error -- runtime export is provided by the installed Playwright package.
import { PNG } from "playwright-core/lib/utilsBundle";

export interface BrowserRequestFact {
  method: string;
  path: string;
}

export interface PixelRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface RealtimeCapture {
  path: "listener" | "onmessage";
  data: string;
}

type CaptureWindow = Window & {
  __btRealtimePayloads?: RealtimeCapture[];
};

export function assertSafeRealPlatformURL(value: string): URL {
  const target = new URL(value);
  const loopback = target.hostname === "127.0.0.1" || target.hostname === "localhost" || target.hostname === "[::1]";
  const safe = (target.protocol === "http:" || target.protocol === "https:")
    && loopback
    && target.port !== ""
    && target.username === ""
    && target.password === ""
    && target.pathname === "/"
    && target.search === ""
    && target.hash === "";
  if (!safe) throw new Error("Real-browser acceptance requires an explicit, non-deceptive loopback Platform URL.");
  return target;
}

export function assertTestSessionMode(value: unknown): void {
  const principal = value && typeof value === "object" && "principal" in value ? value.principal : null;
  const mode = principal && typeof principal === "object" && "mode" in principal ? principal.mode : null;
  if (mode !== "test") throw new Error("Real-browser acceptance requires authoritative exact Test mode.");
}

export function assertNoAuthoritativeAccountWrites(requests: BrowserRequestFact[]): void {
  const allowedWrite = (request: BrowserRequestFact) => request.method === "POST"
    && (/^\/api\/v1\/auth\/login$/.test(request.path) || /^\/api\/v1\/accounts\/[^/]+\/validate$/.test(request.path));
  const forbidden = requests.some((request) => !["GET", "HEAD", "OPTIONS"].includes(request.method) && !allowedWrite(request));
  if (forbidden) throw new Error("Real-browser acceptance emitted an authoritative private or account mutation.");
}

export function installEventSourceCapture(): void {
  const scope = window as CaptureWindow;
  const captures: RealtimeCapture[] = [];
  Object.defineProperty(scope, "__btRealtimePayloads", { configurable: false, value: captures });
  const record = (path: RealtimeCapture["path"], event: Event) => {
    if (event instanceof MessageEvent && typeof event.data === "string") captures.push({ path, data: event.data });
  };

  const nativeAdd = EventSource.prototype.addEventListener as (type: string, listener: EventListenerOrEventListenerObject, options?: boolean | AddEventListenerOptions) => void;
  EventSource.prototype.addEventListener = function (this: EventSource, type: string, listener: EventListenerOrEventListenerObject, options?: boolean | AddEventListenerOptions) {
    nativeAdd.call(this, type, (event: Event) => record("listener", event), options);
    nativeAdd.call(this, type, listener, options);
  } as typeof EventSource.prototype.addEventListener;

  const onmessage = Object.getOwnPropertyDescriptor(EventSource.prototype, "onmessage");
  if (!onmessage?.get || !onmessage.set) throw new Error("EventSource onmessage capture could not be installed.");
  Object.defineProperty(EventSource.prototype, "onmessage", {
    configurable: onmessage.configurable,
    enumerable: onmessage.enumerable,
    get: onmessage.get,
    set(listener: ((this: EventSource, event: MessageEvent) => unknown) | null) {
      if (!listener) {
        onmessage.set!.call(this, listener);
        return;
      }
      onmessage.set!.call(this, function (this: EventSource, event: MessageEvent) {
        record("onmessage", event);
        return listener.call(this, event);
      });
    },
  });
}

function inside(rects: PixelRect[], x: number, y: number): boolean {
  return rects.some((rect) => x >= rect.x && x < rect.x + rect.width && y >= rect.y && y < rect.y + rect.height);
}

function pixelChanged(left: Buffer, right: Buffer, index: number): boolean {
  return left[index] !== right[index]
    || left[index + 1] !== right[index + 1]
    || left[index + 2] !== right[index + 2]
    || left[index + 3] !== right[index + 3];
}

export function assertCanaryPixelIsolation(before: Buffer, withCanary: Buffer, after: Buffer, allowedRects: PixelRect[]): void {
  const baseline = PNG.sync.read(before);
  const entered = PNG.sync.read(withCanary);
  const cleared = PNG.sync.read(after);
  if (baseline.width !== entered.width || baseline.height !== entered.height || baseline.width !== cleared.width || baseline.height !== cleared.height) {
    throw new Error("Full-page credential screenshots changed dimensions.");
  }
  if (baseline.width < 1 || baseline.height < 1 || allowedRects.length === 0) throw new Error("Full-page credential screenshot evidence is incomplete.");

  let insideChanges = 0;
  let outsideChanges = 0;
  let unclearedChanges = 0;
  let variedPixels = 0;
  const firstPixel = cleared.data.subarray(0, 4);
  for (let offset = 0; offset < baseline.data.length; offset += 4) {
    const pixel = offset / 4;
    const x = pixel % baseline.width;
    const y = Math.floor(pixel / baseline.width);
    if (pixelChanged(baseline.data, entered.data, offset)) {
      if (inside(allowedRects, x, y)) insideChanges += 1;
      else outsideChanges += 1;
    }
    if (pixelChanged(baseline.data, cleared.data, offset)) unclearedChanges += 1;
    if (cleared.data[offset] !== firstPixel[0]
      || cleared.data[offset + 1] !== firstPixel[1]
      || cleared.data[offset + 2] !== firstPixel[2]
      || cleared.data[offset + 3] !== firstPixel[3]) variedPixels += 1;
  }
  if (outsideChanges !== 0) throw new Error("Credential canary changed decoded pixels outside controlled inputs.");
  if (insideChanges === 0) throw new Error("Credential canary produced no decoded pixels inside controlled inputs.");
  if (unclearedChanges !== 0) throw new Error("Clearing credentials did not restore the decoded full page.");
  if (variedPixels === 0) throw new Error("Decoded full-page screenshot is visually blank.");
}
