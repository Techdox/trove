import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./test/browser",
  timeout: 30_000,
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:18081",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command: "node test/browser/server.mjs",
    url: "http://127.0.0.1:18081/healthz",
    timeout: 120_000,
    reuseExistingServer: false,
  },
});
