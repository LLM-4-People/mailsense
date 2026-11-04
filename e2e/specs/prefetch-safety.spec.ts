import { agendaView, learningView, priorityView } from "../fixtures/data";
import { mockApi } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { deferred, expect, hoverNav, test } from "../fixtures/test";

// The policy skips only the screens whose render writes module state. A safe
// destination still warms at idle: after the reads are dropped and the Hub
// read is held, its prefetched node paints immediately instead of a skeleton.
test("a safe destination still prefetches", async ({ app }) => {
  await installFakeEventSource(app.page);
  const gate = deferred();
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-10-29T12:00:00Z" } },
    { method: "GET", path: "/api/priority", body: priorityView() },
    { method: "GET", path: "/api/agenda", body: agendaView() },
    { method: "GET", path: "/api/ask/history", body: { items: [] } },
  ]);

  await app.goto("/settings");
  await app.expectRoute("settings");

  // The first hover can land while the mounted render is still counted as
  // in flight, in which case the scheduler drops its once-only marker. The
  // second hover is the normal idle warm a later visit would get.
  await hoverNav(app.page, "hub");
  await app.page.waitForTimeout(400);
  await hoverNav(app.page, "hub");

  // Drop every cached read and stale the route cache, then make the Hub read
  // hang. The prefetched entry is still mounted as-is; a cold render would
  // stay on its skeleton because renderHub awaits this read.
  await app.page.route("**/api/hub*", async (route) => {
    await gate.promise;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ generated_at: "2026-10-29T12:00:00Z" }),
    });
  });
  await emitSse(app.page, "config.changed", { keys: ["mail.sync"] });

  try {
    await app.page.locator('[data-nav="hub"]').click();
    await expect(app.page.locator(".home-head-heading")).toBeVisible({ timeout: 3_000 });
    await app.expectRoute("hub");
  } finally {
    gate.resolve();
  }
});
