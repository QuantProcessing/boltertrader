/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The dev server never talks to the gateway or to trading directly. Everything
// goes through the auth edge, because the auth edge is what turns a browser
// session into the service token and principal header the downstream services
// require — bypassing it in development would mean developing against an
// authorization path that does not exist in production.
const AUTH_EDGE = process.env.BT_WEB_API_ORIGIN ?? "http://127.0.0.1:18091";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "/api": {
        target: AUTH_EDGE,
        changeOrigin: false,
        // The realtime stream is server-sent events. Buffering it would hold
        // every event until the connection closed, which is never.
        configure: (proxy) => {
          proxy.on("proxyRes", (proxyRes) => {
            if (proxyRes.headers["content-type"]?.includes("text/event-stream")) {
              proxyRes.headers["cache-control"] = "no-cache, no-transform";
            }
          });
        },
      },
    },
  },
  build: {
    target: "es2023",
    sourcemap: true,
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
});
