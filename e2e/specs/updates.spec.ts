import { fulfillJson, mockApi } from "../fixtures/mock";
import { installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

function updateStatus(overrides: Record<string, unknown> = {}) {
  return {
    current: "0.3.0-dev+test",
    latest: "0.4.0",
    available: true,
    versions_behind: 1,
    release_url: "https://example.invalid/mailsense/releases/tag/v0.4.0",
    notes: "## What changed\n\n- A new update screen and a first-open note.\n- The Settings tab names the current version.",
    notes_seen: true,
    skipped: false,
    check_enabled: true,
    checked_at: "2026-11-05T10:00:00Z",
    ...overrides,
  };
}

test("the Updates tab shows the release, skips it and checks again", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  const posts = { check: 0, skip: "" };
  let current = updateStatus();
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/update",
      handler: async ({ route }) => {
        await fulfillJson(route, current);
      },
    },
    {
      method: "POST",
      path: "/api/update/check",
      handler: async ({ route }) => {
        posts.check += 1;
        current = updateStatus({
          latest: "",
          available: false,
          versions_behind: 0,
          checked_at: "2026-11-05T11:00:00Z",
        });
        await fulfillJson(route, current);
      },
    },
    {
      method: "POST",
      path: "/api/update/skip",
      handler: async ({ route, request }) => {
        posts.skip = (request.postDataJSON() as { version: string }).version;
        current = updateStatus({ skipped: true });
        await fulfillJson(route, current);
      },
    },
  ]);

  await app.goto("/settings?tab=updates");
  await app.expectRoute("settings");

  const panel = app.page.locator(".update-panel");
  await expect(panel).toContainText("Current version");
  await expect(panel).toContainText("0.3.0-dev+test");
  await expect(panel).toContainText("0.4.0");
  await expect(panel).toContainText("Release notes");
  // The one persistent signal survives leaving the tab.
  await expect(app.page.locator("#nav-settings-update")).toBeVisible();
  await expect(app.page.locator("#nav-settings-update-text")).toHaveText(
    "Update available",
  );
  // The three schema rows are owned by this tab, not repeated on General.
  await expect(
    panel.locator(".setting-row").filter({ hasText: "update.check.interval" }),
  ).toBeVisible();

  await app.page.getByRole("button", { name: "Skip this version" }).click();
  await expect.poll(() => posts.skip).toBe("0.4.0");
  await expect(panel).toContainText("Version 0.4.0 is skipped.");
  await expect(app.page.locator("#nav-settings-update")).toBeHidden();

  const checkButton = app.page.getByRole("button", { name: "Check now" });
  await checkButton.focus();
  await app.page.keyboard.press("Enter");
  await expect.poll(() => posts.check).toBe(1);
  await expect(panel).toContainText("You have the latest version.");
  // The refresh replaces the button; the keyboard owner keeps its place.
  await expect(app.page.locator("#update-check")).toBeFocused();
  await expect(
    app.page.getByRole("button", { name: "Skip this version" }),
  ).toHaveCount(0);
});

test("a failed check stays a quiet status, not a broken panel", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/update",
      handler: async ({ route }) => {
        await fulfillJson(route, updateStatus({
          available: false,
          latest: "",
          versions_behind: 0,
          error_code: "rate_limited",
          last_error: "slow down",
        }));
      },
    },
  ]);
  await app.goto("/settings?tab=updates");
  await app.expectRoute("settings");
  const panel = app.page.locator(".update-panel");
  await expect(panel).toContainText("The last check failed: slow down");
  await expect(panel.locator(".update-headline")).toHaveText(
    "The last check failed: slow down",
  );
});

test("the first open shows What's New and marks the notes seen", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  let current = updateStatus({ notes_seen: false });
  const seen: string[] = [];
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/update",
      handler: async ({ route }) => {
        await fulfillJson(route, current);
      },
    },
    {
      method: "POST",
      path: "/api/update/seen",
      handler: async ({ route, request }) => {
        seen.push((request.postDataJSON() as { version: string }).version);
        current = updateStatus({ notes_seen: true });
        await fulfillJson(route, current);
      },
    },
  ]);

  await app.goto("/hub");
  await app.expectRoute("hub");
  const modal = app.page.locator("#modal");
  await expect(modal).toBeVisible();
  await expect(modal.locator("#modal-title")).toHaveText(
    "What's new in 0.4.0",
  );
  await expect(modal).toContainText("A new update screen");
  // The release body is rendered, not shown as raw Markdown.
  await expect(modal).not.toContainText("##");
  await expect(modal.locator(".update-notes-body h3").first()).toHaveText(
    "What changed",
  );
  await expect(modal.locator(".update-notes-body li")).toHaveCount(2);
  await modal.getByRole("button", { name: "Got it" }).click();
  await expect(modal).toBeHidden();
  await expect.poll(() => seen).toEqual(["0.4.0"]);
  // Dismissing the notes keeps the persistent Settings affordance.
  await expect(app.page.locator("#nav-settings-update")).toBeVisible();
});

for (const [label, info] of [
  ["the notes are seen", updateStatus({ notes_seen: true })],
  ["the version is skipped", updateStatus({ skipped: true })],
  ["no release is offered", updateStatus({ available: false, latest: "" })],
] as const) {
  test(`no What's New overlay when ${label}`, async ({ app }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/update",
        handler: async ({ route }) => {
          await fulfillJson(route, info);
        },
      },
    ]);
    const read = app.page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === "/api/update" &&
        response.request().method() === "GET",
    );
    await app.goto("/hub");
    await app.expectRoute("hub");
    await read;
    await expect(app.page.locator("#modal")).toBeHidden();
  });
}
