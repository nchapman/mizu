import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import "./index.css";
import { applyTheme, loadThemePref } from "./lib/theme";

// Apply the saved theme before React renders so first paint matches the
// final theme — otherwise the user sees a light-mode flash on dark setups.
applyTheme(loadThemePref());

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
