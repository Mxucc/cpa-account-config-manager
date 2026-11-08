import React from "react";
import ReactDOM from "react-dom/client";
import { V2App } from "./v2/App";
import { I18nProvider } from "./i18n";
import { initFontSize } from "./store/fontSize";
import { initThemeSync } from "./store/theme";
import { initPluginTheme } from "./store/pluginTheme";
import "./styles/v2.css";
import "./styles.css";

initThemeSync();
initFontSize();
initPluginTheme();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <I18nProvider><V2App /></I18nProvider>
  </React.StrictMode>,
);
