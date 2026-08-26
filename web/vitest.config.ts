import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // Account/inspection integration tests exercise several async host API
    // flows and can legitimately take longer than Vitest's 5s default when
    // the CI runner is under load. Keep the timeout explicit so verification
    // is deterministic instead of failing on scheduler jitter.
    testTimeout: 15_000,
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
