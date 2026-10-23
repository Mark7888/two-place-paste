import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("the page has no root element");

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

// The service worker makes the admin installable and lets it open offline
// (sw/sw.js). Browsers run one only on HTTPS or localhost; on a plain-http
// relay the page works as before, without it. The dev server has none.
if (import.meta.env.PROD && "serviceWorker" in navigator) {
  navigator.serviceWorker.register("/admin/sw.js", { scope: "/admin/" }).catch(() => undefined);
}
