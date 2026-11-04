import { defineConfig } from "@playwright/test";

// The suite boots the real Mailsense binary per worker. Content endpoints
// that need mail are mocked with synthetic fixtures; everything the shell
// itself depends on (token, auth, CSP, static assets, real SSE, 401) stays
// real. Traces and video are off on purpose: the launch token travels in
// the SSE query string and a trace would persist it.
export default defineConfig({
  testDir: "./specs",
  globalSetup: "./global-setup.ts",
  timeout: 45_000,
  expect: { timeout: 8_000 },
  fullyParallel: true,
  // The suite is fully parallel and every worker boots its own server; the
  // host has 12 cores, and the slowest spec (a 20 s TTL wait) bounds the wall
  // time past 8 workers.
  workers: process.env.CI ? 4 : 8,
  retries: 0,
  reporter: [
    ["list"],
    ["html", { open: "never", outputFolder: "playwright-report" }],
  ],
  outputDir: "test-results",
  use: {
    locale: "en-US",
    timezoneId: "UTC",
    actionTimeout: 10_000,
    navigationTimeout: 20_000,
    trace: "off",
    video: "off",
    screenshot: "only-on-failure",
  },
});
