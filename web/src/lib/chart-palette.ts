/* ---------------------------------------------------------------------------
 * Design tokens, resolved to colours a charting library can parse.
 *
 * lightweight-charts parses colours with its own parser, which knows hex,
 * `rgb()` and `hsl()` and nothing newer — handed the `oklch()` the tokens are
 * written in it throws, and a throw inside `createChart` takes the whole route
 * down with it. So every token that reaches a chart passes through here first.
 *
 * Read from the live computed style rather than hard-coded, so no chart can
 * drift from `theme.css`: there is exactly one definition of "up" in this
 * product and it is not in a chart file. It follows that this must be called
 * during an effect, once the stylesheet is attached and the custom properties
 * actually resolve.
 *
 * The token names below are load-bearing. `theme.css` is free to change what a
 * token means, but renaming one without changing it here fails silently —
 * `getPropertyValue` returns "" for an unknown property, the canvas paints the
 * `#000000` seed, and the chart goes black on black. `design.md` §11.8 states
 * this as a rule; this comment is where it bites.
 * ------------------------------------------------------------------------- */

export interface ChartPalette {
  dim: string;
  line: string;
  lineStrong: string;
  raised: string;
  accent: string;
  up: string;
  down: string;
  upFaint: string;
  downFaint: string;
}

export function readChartPalette(): ChartPalette {
  const styles = getComputedStyle(document.documentElement);
  const token = (name: string) => srgb(styles.getPropertyValue(name).trim());
  const up = token("--bt-up");
  const down = token("--bt-down");
  return {
    dim: token("--bt-dim"),
    line: token("--bt-line"),
    lineStrong: token("--bt-line-strong"),
    raised: token("--bt-raised"),
    accent: token("--bt-accent"),
    up,
    down,
    // The faint pair sits behind whatever it annotates — volume under price,
    // a fill under a line — and must not compete with it.
    upFaint: fadeColor(up, 0.45),
    downFaint: fadeColor(down, 0.45),
  };
}

/**
 * Hands the browser a colour and takes back the sRGB bytes it would paint.
 *
 * Reading a painted pixel back is the only conversion that cannot fall behind
 * the colour syntax: `fillStyle` echoes `oklch()` straight back, but a pixel is
 * always eight bits per channel.
 */
let swatch: CanvasRenderingContext2D | null | undefined;

function channels(color: string): [number, number, number, number] | null {
  swatch ??= document.createElement("canvas").getContext("2d", { willReadFrequently: true });
  if (!swatch) return null;
  swatch.clearRect(0, 0, 1, 1);
  // Assigning an unparseable value is a no-op, so a colour this browser does
  // not understand paints the seed rather than whatever the last token was.
  swatch.fillStyle = "#000000";
  swatch.fillStyle = color;
  swatch.fillRect(0, 0, 1, 1);
  const [red = 0, green = 0, blue = 0, alpha = 255] = swatch.getImageData(0, 0, 1, 1).data;
  return [red, green, blue, alpha];
}

function srgb(color: string): string {
  const parts = channels(color);
  if (!parts) return color;
  const [red, green, blue, alpha] = parts;
  return alpha === 255
    ? `rgb(${red}, ${green}, ${blue})`
    : `rgba(${red}, ${green}, ${blue}, ${(alpha / 255).toFixed(3)})`;
}

/** Alpha applied here rather than with `color-mix()`, for the same reason. */
export function fadeColor(color: string, alpha: number): string {
  const parts = channels(color);
  if (!parts) return color;
  const [red, green, blue] = parts;
  return `rgba(${red}, ${green}, ${blue}, ${alpha})`;
}
