import { defineConfig } from "@playwright/test";

// The documentation captures are their own Playwright project, so the default
// `npx playwright test` never rewrites a committed image. Run them with:
//
//   cd e2e && npx playwright test --config screenshots/playwright.config.ts
//
// The viewport, locale and zone are fixed here; the date and the data are
// fixed in capture.spec.ts and data.ts. A capture that differs between two
// runs is a defect.
export default defineConfig({
  testDir: ".",
  testMatch: /capture\.spec\.ts/,
  globalSetup: "../global-setup.ts",
  timeout: 240_000,
  expect: { timeout: 15_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  outputDir: "test-results",
  use: {
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "en-US",
    timezoneId: "Europe/Paris",
    colorScheme: "light",
    reducedMotion: "reduce",
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    trace: "off",
    video: "off",
    screenshot: "off",
  },
});
