/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Dev server forwards /api (REST + WebSocket) to a running trader. The Host
// header is preserved so the trader's same-origin check sees the dev origin.
const TRADER = process.env.TRADER_ORIGIN ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: { "/api": { target: TRADER, ws: true, changeOrigin: false } },
  },
  build: { target: "es2023" },
  test: { environment: "jsdom", include: ["src/**/*.test.ts"] },
});
