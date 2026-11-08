import type { Config } from "tailwindcss";

// Corporate Clean tokens. The palette and motion limits are the design contract:
// blue-600/700 for primary actions, slate-50 surfaces, gray borders, shadows no
// heavier than shadow-md, and no gradients anywhere.
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      fontFamily: {
        sans: [
          "ui-sans-serif",
          "system-ui",
          "-apple-system",
          "Segoe UI",
          "PingFang SC",
          "Hiragino Sans GB",
          "Microsoft YaHei",
          "sans-serif",
        ],
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
      transitionDuration: {
        DEFAULT: "150ms",
      },
    },
  },
  corePlugins: {
    // The legacy stylesheet still owns the old workspaces embedded as advanced
    // panels; preflight would reset their form controls.
    preflight: false,
  },
  plugins: [],
} satisfies Config;
