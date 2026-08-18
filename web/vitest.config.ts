import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "jsdom",
    environmentOptions: {
      jsdom: {
        // JSDOM without an origin rejects localStorage. The plugin is always
        // served from CPA's loopback HTTP origin, so make that explicit.
        url: "http://127.0.0.1:8317/",
      },
    },
    setupFiles: "./src/test/setup.ts",
    css: true,
    restoreMocks: true,
  },
});
