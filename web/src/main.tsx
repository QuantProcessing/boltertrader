import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "@/app/App";
import "@/styles/app.css";

const container = document.getElementById("root");

if (!container) {
  throw new Error("#root is missing from index.html");
}

const root = createRoot(container);

root.render(
  <StrictMode>
    <App />
  </StrictMode>,
);
