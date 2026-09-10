import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
const css = readFileSync(resolve(process.cwd(), "src/styles/theme.css"), "utf8");
const palettes = [...css.matchAll(/:root(?:\[data-theme="light"\])?\s*\{([^}]+)\}/g)].filter(match => match[1]!.includes("--bt-text"));
function luminance(hex: string) { const rgb = hex.slice(1).match(/../g)!.map(value => { const c = parseInt(value,16)/255; return c <= .04045 ? c/12.92 : ((c+.055)/1.055)**2.4; }); return rgb[0]!*.2126+rgb[1]!*.7152+rgb[2]!*.0722; }
for (const [i, match] of palettes.entries()) {
 const values = Object.fromEntries([...match[1]!.matchAll(/--bt-([\w-]+):\s*(#[\da-f]{6})/gi)].map(m => [m[1]!,m[2]!]));
 function contrast(a: string,b: string) { const x=luminance(values[a]!), y=luminance(values[b]!); return (Math.max(x,y)+.05)/(Math.min(x,y)+.05); }
 describe(i === 0 ? "dark contrast" : "light contrast", () => {
  it.each(["text","muted","dim","up","down","warn","danger","manual"])("keeps %s readable on raised", token => expect(contrast(token,"raised")).toBeGreaterThanOrEqual(4.5));
  it.each(["buy","sell","accent"])("keeps %s action labels readable", token => expect(contrast(`${token}-fg`,token)).toBeGreaterThanOrEqual(4.5));
 });
}
it("checks both themes",()=>expect(palettes).toHaveLength(2));
