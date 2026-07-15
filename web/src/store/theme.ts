type Theme = "light" | "white" | "dark";

function embedded(): boolean {
  try {
    return window.self !== window.top;
  } catch {
    return false;
  }
}

function parentTheme(): Theme {
  if (!embedded()) return "light";
  try {
    const value = window.parent.document.documentElement.getAttribute("data-theme");
    return value === "dark" || value === "white" ? value : "light";
  } catch {
    return "light";
  }
}

function applyTheme(theme: Theme): void {
  if (theme === "light") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", theme);
}

export function initThemeSync(): () => void {
  applyTheme(parentTheme());
  if (!embedded()) return () => undefined;
  try {
    const element = window.parent.document.documentElement;
    const observer = new MutationObserver(() => applyTheme(parentTheme()));
    observer.observe(element, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  } catch {
    return () => undefined;
  }
}
