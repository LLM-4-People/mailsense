import { learningView, statusView } from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

test.describe("time zone", () => {
  test("the first run confirms the browser's IANA zone", async ({ app }) => {
    const view = learningView();
    view.status.stages = [
      {
        id: "machine",
        state: "done",
        progress: { done: 1, total: 1, unit: "facts" },
        blockers: [{ code: "time_zone_unconfirmed" }],
      },
      ...view.status.stages,
    ];
    await mockApi(app.page, [
      { method: "GET", path: "/api/learning", body: view },
      {
        method: "POST",
        path: "/api/learning/zone",
        handler: async ({ route }) => {
          await fulfillJson(route, {});
        },
      },
    ]);

    const confirmed = app.page.waitForRequest(
      (request) =>
        new URL(request.url()).pathname === "/api/learning/zone" &&
        request.method() === "POST",
    );
    await app.goto("/hub");
    const body = (await confirmed).postDataJSON() as { zone?: string };
    expect(typeof body.zone).toBe("string");
    expect((body.zone || "").length).toBeGreaterThan(0);
  });

  test.describe("first-run confirmation", () => {
    // The browser posts the zone it reports, so the status reload can return
    // a different confirmed zone and prove the shell adopted it.
    test.use({ timezoneId: "America/New_York" });

    test("confirms once and does not repeat after a later update", async ({ app }) => {
      await installFakeEventSource(app.page);
      const view = learningView();
      view.status.stages = [
        {
          id: "machine",
          state: "blocked",
          progress: { done: 0, total: 1, unit: "facts" },
          blockers: [{ code: "time_zone_unconfirmed" }],
        },
        ...view.status.stages,
      ];
      let confirmed = false;
      let statusReads = 0;
      const postedZones: string[] = [];

      await mockApi(app.page, [
        { method: "GET", path: "/api/learning", body: view },
        {
          method: "GET",
          path: "/api/status",
          handler: async ({ route }) => {
            statusReads += 1;
            await fulfillJson(route, {
              version: "e2e",
              time_zone: confirmed ? "America/New_York" : "UTC",
              data_dir: "/tmp/e2e",
            });
          },
        },
        {
          method: "POST",
          path: "/api/learning/zone",
          handler: async ({ route, request }) => {
            const body = request.postDataJSON() as { zone?: string };
            postedZones.push(String(body.zone || ""));
            confirmed = true;
            await fulfillJson(route, {});
          },
        },
      ]);

      const zoneToast = app.page
        .locator("#toasts .toast")
        .filter({ hasText: "Time zone set to America/New_York" });
      await app.goto("/hub");
      await app.expectRoute("hub");
      await expect(app.page.locator(".home-screen")).toBeVisible();
      await expect(zoneToast).toHaveCount(1);
      expect(postedZones).toEqual(["America/New_York"]);
      expect(statusReads).toBeGreaterThanOrEqual(2);
      await expect
        .poll(() =>
          app.page.evaluate(
            () =>
              (
                window as unknown as {
                  MailsenseI18n: {
                    snapshot(): { zone: { zone: string; source: string } };
                  };
                }
              ).MailsenseI18n.snapshot().zone.zone,
          ),
        )
        .toBe("America/New_York");

      // A later learning/status push must not add a second POST or toast:
      // the page has already used its one automatic confirmation.
      const before = statusReads;
      await emitSse(app.page, "learning.status", {
        ...view.status,
        stages: view.status.stages.map((stage) =>
          stage.id === "machine" ? { ...stage, state: "running" } : stage,
        ),
      });
      const learningRefreshed = app.page.waitForResponse(
        (response) => new URL(response.url()).pathname === "/api/learning",
      );
      const hubRefreshed = app.page.waitForResponse(
        (response) => new URL(response.url()).pathname === "/api/hub",
      );
      await emitSse(app.page, "index.finished");
      await learningRefreshed;
      await hubRefreshed;

      expect(postedZones).toEqual(["America/New_York"]);
      expect(statusReads).toBe(before);
      await expect(zoneToast).toHaveCount(1);
    });
  });
});

// The manual dialog is the second confirmation path: after the automatic
// attempt fails, the owner confirms the zone themselves. The action must
// scope its invalidation to the zone reads, not wipe every cached screen.
test("the manual zone dialog scopes its refresh", async ({ app }) => {
  await installFakeEventSource(app.page);
  const view = learningView();
  view.status.stages = [
    {
      id: "machine",
      state: "blocked",
      progress: { done: 0, total: 1, unit: "facts" },
      blockers: [
        {
          code: "time_zone_unconfirmed",
          actions: [
            {
              id: "confirm_time_zone",
              params: { iana: "America/New_York", offset: "UTC-5", abbrev: "EST" },
            },
          ],
        },
      ],
    },
    ...view.status.stages,
  ];
  let posts = 0;
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: view },
    {
      method: "GET",
      path: "/api/status",
      body: statusView(),
    },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-11-03T10:00:00Z" } },
    {
      method: "POST",
      path: "/api/learning/zone",
      handler: async ({ route }) => {
        posts += 1;
        if (posts === 1) {
          await fulfillJson(
            route,
            { error: { code: "internal", message: "synthetic zone failure" } },
            500,
          );
          return;
        }
        await fulfillJson(route, {});
      },
    },
  ]);

  await app.goto("/learning");
  await app.expectRoute("learning");
  await expect(app.page.locator(".learning-screen")).toBeVisible();
  // The boot path already used (and failed) its one automatic attempt.
  await expect.poll(() => posts).toBe(1);

  await app.page.getByRole("button", { name: "Confirm time zone" }).first().click();
  const modal = app.page.locator("#modal");
  await expect(modal).toBeVisible();
  await expect(modal.locator("#modal-title")).toHaveText("Confirm time zone");
  await modal.getByRole("button", { name: "Confirm zone" }).click();

  await expect(
    app.page.locator("#toasts .toast").filter({ hasText: "Time zone confirmed" }),
  ).toHaveCount(1);
  await expect.poll(() => posts).toBe(2);
  await expect(app.page.locator(".learning-screen")).toBeVisible();
});
