import { mockApi } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { expect, test, type App } from "../fixtures/test";

function logSnapshot(entry: Record<string, unknown>) {
  return {
    entries: [entry],
    health: { file_errors: 0, file_failing: false, last_file_error: "", bus_dropped: 0 },
    oldest_seq: entry.seq,
    last_seq: entry.seq,
    truncated: false,
  };
}

async function openLogs(app: App) {
  await app.goto("/logs");
  await app.expectRoute("logs");
  await expect(
    app.page.locator("#log-rows table").or(app.page.locator("#log-rows .empty")).first(),
  ).toBeVisible();
}

async function unauthorized(app: App): Promise<void> {
  const response = await app.page.request.get(app.server.baseURL + "api/status");
  expect(response.status()).toBe(401);
}

test.describe("Logs", () => {
  test("a real server event appears live and filters apply to what is shown", async ({
    app,
  }) => {
    await openLogs(app);
    await unauthorized(app);

    const entry = app.page.locator(".log-row").filter({ hasText: "api unauthorized" });
    await expect(entry.first()).toBeVisible();
    await expect(entry.first().locator(".log-level")).toHaveText("WARN");

    await app.page.getByLabel("Level filter").selectOption("warn");
    await expect(app.page.locator(".log-level").first()).toHaveText("WARN");
    for (const level of await app.page.locator(".log-level").allInnerTexts()) {
      expect(level).toBe("WARN");
    }

    await app.page.locator("#global-search-input").fill("api");
    // The filter state updates on every keystroke; the thousand-row table is
    // rebuilt once after the debounce, so the assertion waits for the settled
    // view instead of sampling mid-keystroke.
    await expect
      .poll(async () => {
        const components = await app.page.locator(".log-component").allInnerTexts();
        return (
          components.length > 0 &&
          components.every((value) => value.toLowerCase().includes("api"))
        );
      })
      .toBe(true);
  });

  test("pause holds new rows until resume, and clear empties the view", async ({
    app,
  }) => {
    await openLogs(app);
    await unauthorized(app);
    await expect(
      app.page.locator(".log-row").filter({ hasText: "api unauthorized" }).first(),
    ).toBeVisible();

    // Let any entry already in flight paint before the pause boundary.
    await app.page.waitForTimeout(300);
    await app.page.getByRole("button", { name: "Pause" }).click();
    const paused = await app.page.locator(".log-row").count();
    await unauthorized(app);
    await app.page.waitForTimeout(700);
    expect(await app.page.locator(".log-row").count()).toBe(paused);
    await expect(app.page.locator("#log-meta")).toContainText("paused");

    await app.page.getByRole("button", { name: "Resume" }).click();
    await expect
      .poll(() => app.page.locator(".log-row").count())
      .toBeGreaterThan(paused);

    await app.page.getByRole("button", { name: "Clear view" }).click();
    await expect(app.page.locator(".log-row")).toHaveCount(0);
    await expect(app.page.locator("#log-rows .empty")).toBeVisible();
  });

  test("a page omits attributes and one expansion reads its entry once", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    const pageReads: string[] = [];
    const entryReads: string[] = [];
    app.page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname === "/api/logs") pageReads.push(url.search);
      if (url.pathname === "/api/logs/41") entryReads.push(url.pathname);
    });
    let release: () => void = () => {};
    const detail = new Promise<void>((resolve) => {
      release = resolve;
    });
    const entry = {
      seq: 41,
      time: "2026-10-30T09:00:00Z",
      level: "INFO",
      component: "test",
      msg: "lazy attributes",
      has_attrs: true,
    };
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/logs",
        query: { attrs: "off" },
        body: logSnapshot(entry),
      },
      {
        method: "GET",
        path: "/api/logs/41",
        handler: async ({ route }) => {
          await detail;
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              ...entry,
              attrs: { attempt: 2, mailbox: "owner@example.test" },
            }),
          });
        },
      },
    ]);

    await openLogs(app);
    expect(pageReads.length).toBeGreaterThan(0);
    expect(pageReads[0]).toContain("attrs=off");
    expect(pageReads[0]).not.toContain("since=");
    await expect(app.page.locator("#log-attrs-41")).toHaveCount(0);

    await app.page.getByRole("button", { name: "Show attributes" }).click();
    const attrs = app.page.locator("#log-attrs-41");
    await expect(attrs).toContainText("Loading attributes");
    expect(entryReads.length).toBe(1);

    // A live wake reads the tail with the same attrs=off rule, replacing the
    // map's entry while the one-entry read is still in flight.
    await emitSse(app.page, "log.wake", { count: 1 });
    await expect.poll(() => pageReads.length).toBeGreaterThan(1);
    expect(pageReads.some((search) => search.includes("since=") && search.includes("attrs=off"))).toBe(true);
    // Let the tail response replace the map entry before the detail lands.
    await app.page.waitForTimeout(250);
    release();

    await expect(attrs).toContainText("attempt=2");
    await expect(attrs).toContainText("mailbox=owner@example.test");

    // The row keeps the fetched map: collapse and re-expand read nothing.
    await app.page.getByRole("button", { name: "Hide attributes" }).click();
    await expect(app.page.locator("#log-attrs-41")).toHaveCount(0);
    await app.page.getByRole("button", { name: "Show attributes" }).click();
    await expect(app.page.locator("#log-attrs-41")).toContainText("attempt=2");
    await app.page.waitForTimeout(200);
    expect(entryReads.length).toBe(1);
  });

  test("an entry that rotated out shows the retained message", async ({ app }) => {
    await installFakeEventSource(app.page);
    const entryReads: string[] = [];
    app.page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/logs/7") entryReads.push("/api/logs/7");
    });
    const entry = {
      seq: 7,
      time: "2026-10-30T09:00:00Z",
      level: "WARN",
      component: "test",
      msg: "rotated attributes",
      has_attrs: true,
    };
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/logs",
        query: { attrs: "off" },
        body: logSnapshot(entry),
      },
      {
        method: "GET",
        path: "/api/logs/7",
        status: 404,
        body: { error: { code: "log_not_found", message: "no such log entry" } },
      },
    ]);

    await openLogs(app);
    await app.page.getByRole("button", { name: "Show attributes" }).click();
    await expect(app.page.locator("#log-attrs-7")).toContainText("Attributes are no longer retained.");
    expect(entryReads.length).toBe(1);

    await app.page.getByRole("button", { name: "Hide attributes" }).click();
    await app.page.getByRole("button", { name: "Show attributes" }).click();
    await expect(app.page.locator("#log-attrs-7")).toContainText("Attributes are no longer retained.");
    await app.page.waitForTimeout(200);
    expect(entryReads.length).toBe(1);
  });
});

test("a Logs remount reads only the entries it does not already hold", async ({
  app,
}) => {
  const reads: string[] = [];
  app.page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/logs") reads.push(url.search);
  });
  await openLogs(app);
  await expect(app.page.locator("#log-rows table")).toBeVisible();
  expect(reads.length).toBeGreaterThan(0);
  expect(reads[0]).not.toContain("since=");

  // A cached route restores without a read; the toolbar Refresh re-runs the
  // screen, which is the read this test is about.
  await app.page.getByRole("button", { name: "Refresh" }).click();
  await expect(app.page.locator("#log-rows table")).toBeVisible();
  // The retained ring is the cache: the second read names the newest entry
  // it already holds instead of taking the whole page again.
  await expect.poll(() => reads.length).toBeGreaterThan(1);
  expect(reads.some((search) => search.includes("since="))).toBe(true);
});

// Only a 404 means the entry rotated out of the retained ring. A transport
// or server failure leaves the attributes unknown, keeps the row expanded and
// offers an inline retry instead of claiming the entry is gone.
test("a failed attribute read offers a retry instead of claiming the entry is gone", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  let reads = 0;
  const entry = {
    seq: 51,
    time: "2026-10-30T09:00:00Z",
    level: "INFO",
    component: "test",
    msg: "attribute transport failure",
    has_attrs: true,
  };
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/logs",
      query: { attrs: "off" },
      body: logSnapshot(entry),
    },
    {
      method: "GET",
      path: "/api/logs/51",
      handler: async ({ route }) => {
        reads += 1;
        if (reads === 1) {
          await route.fulfill({
            status: 500,
            contentType: "application/json",
            body: JSON.stringify({
              error: { code: "internal", message: "synthetic attribute failure" },
            }),
          });
          return;
        }
        await route.fulfill({
          status: 404,
          contentType: "application/json",
          body: JSON.stringify({
            error: { code: "log_not_found", message: "no such log entry" },
          }),
        });
      },
    },
  ]);

  await openLogs(app);
  await app.page.getByRole("button", { name: "Show attributes" }).click();
  const attrs = app.page.locator("#log-attrs-51");
  await expect(attrs).toContainText("synthetic attribute failure");
  await expect(attrs).not.toContainText("Attributes are no longer retained.");

  await attrs.getByRole("button", { name: "Try again" }).click();
  await expect(attrs).toContainText("Attributes are no longer retained.");
  expect(reads).toBe(2);
});
