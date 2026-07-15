import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { viteSingleFile } from "vite-plugin-singlefile";
import { resolve } from "node:path";

const cpaBase = process.env.VITE_CPA_BASE ?? "http://127.0.0.1:8317";

export default defineConfig(({ mode }) => {
  const hosted = mode === "hosted" || process.env.VITE_HOSTED === "1";

  return {
    base: hosted ? "/v0/resource/plugins/cpa-account-config-manager/" : "/",
    plugins: [react(), viteSingleFile()],
    build: {
      outDir: resolve(__dirname, "../internal/web/dist"),
      emptyOutDir: true,
      assetsInlineLimit: 100_000_000,
      cssCodeSplit: false,
      rollupOptions: {
        output: { inlineDynamicImports: true },
      },
    },
    server: {
      host: "127.0.0.1",
      port: 5175,
      proxy: {
        "/v0/management": { target: cpaBase, changeOrigin: true },
      },
    },
  };
});
