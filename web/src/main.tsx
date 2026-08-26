import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { I18nProvider } from "./i18n";
import { initFontSize } from "./store/fontSize";
import { initThemeSync } from "./store/theme";
import { initPluginTheme } from "./store/pluginTheme";
import "./styles.css";

initThemeSync();
initFontSize();
initPluginTheme();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <I18nProvider><App /></I18nProvider>
  </React.StrictMode>,
);
