import { test as base, expect, type Page } from "@playwright/test";
import { startServer, stopServer, type Server } from "./server";

export type App = {
  page: Page;
  server: Server;
  /** Navigate to a hash route (`/ask`, `#/mail?mode=search`) with a fresh page. */
  goto(route: string): Promise<void>;
  /** Wait for the router to mount the named screen. */
  expectRoute(screen: string): Promise<void>;
  /** Wait for the real SSE connection to report itself live. */
  expectLive(): Promise<void>;
};

// One real server per worker. Each worker gets its own data directory and
// launch token; tests in a worker share it, so they must not mutate global
// server settings. Content routes are mocked per page.
export const test = base.extend<{ app: App }, { server: Server }>({
  server: [
    async ({}, use) => {
      const server = await startServer();
      try {
        await use(server);
      } finally {
        await stopServer(server);
      }
    },
    { scope: "worker" },
  ],

  app: async ({ page, server }, use) => {
    // The shipped launcher passes the token in the fragment. Seeding the
    // same session-storage key is the equivalent bootstrap for a test and
    // keeps the token out of every URL and trace.
    await page.addInitScript(
      ({ token }) => {
        try {
          sessionStorage.setItem("mailsense.token", token);
        } catch {
          // about:blank and opaque origins cannot use storage.
        }
      },
      { token: server.token },
    );

    const app: App = {
      page,
      server,
      async goto(route: string) {
        const suffix = route.startsWith("#")
          ? route
          : "#" + (route.startsWith("/") ? route : "/" + route);
        await page.goto(server.baseURL + suffix);
        await expect(page.locator("#app")).toBeVisible();
        await expect(page.locator("#token-screen")).toBeHidden();
      },
      async expectRoute(screen: string) {
        await expect(page.locator("html")).toHaveAttribute(
          "data-screen",
          screen,
        );
      },
      async expectLive() {
        await expect(page.locator("#connection-text")).toHaveText(
          /Live updates/,
        );
      },
    };
    await use(app);
  },
});

export { expect };

// hoverNav moves the pointer and focus onto one sidebar destination, then
// waits for the idle route prefetch its link schedules. The wait is the
// policy's observable window: a prefetch that renders has started by then.
export async function hoverNav(page: Page, screen: string): Promise<void> {
  const nav = page.locator(`[data-nav="${screen}"]`);
  await nav.hover();
  await nav.focus();
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        const done = () => resolve();
        if (typeof window.requestIdleCallback === "function") {
          window.requestIdleCallback(done, { timeout: 2_000 });
        } else {
          window.setTimeout(done, 50);
        }
      }),
  );
  await page.waitForTimeout(100);
}

// deferred is a one-shot gate: the test awaits the promise, and the page
// handler resolves it after the test observed the in-flight state.
export function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve: () => void = () => {};
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
