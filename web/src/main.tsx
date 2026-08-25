import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "@/app/App";
import "@/styles/app.css";

async function enableMocking(): Promise<void> {
  if (import.meta.env.VITE_BOLTER_MOCK !== "1") return;
  const { installMockRealtime, startWorker } = await import("@/mocks/browser");
  installMockRealtime();
  await startWorker();
}

const container = document.getElementById("root");

if (!container) {
  throw new Error("#root is missing from index.html");
}

void enableMocking().then(() => {
  const root = createRoot(container);
  root.render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
});
