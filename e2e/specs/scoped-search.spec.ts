import { mockCompanies } from "../fixtures/companies";
import { searchView } from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { installFakeEventSource } from "../fixtures/sse";
import { mockSettings, simpleKeys } from "../fixtures/settings";
import { expect, test, type App } from "../fixtures/test";

// The one shell search box is the screen's filter. These tests pin the
// registration contract: a cached screen never runs its render again, so the
// router must restore its placeholder, accessible name and value from the
// route's descriptor instead of leaving the global "Search all mail" bar.

const COMPANIES_PLACEHOLDER = "Search all companies";
const COMPANIES_ARIA = "Search companies by name or domain";
const SETTINGS_PLACEHOLDER = "Search all settings";
const SETTINGS_ARIA = "Search settings";
const LOGS_PLACEHOLDER = "component";
const LOGS_ARIA = "Component filter";

async function expectOneVisibleSearchInput(app: App) {
  // A cached screen must restore the shell bar, never append a second visible
  // copy of the hidden proxy input.
  await expect(app.page.locator('input[type="search"]:visible')).toHaveCount(1);
  await expect(app.page.locator("#global-search-input")).toBeVisible();
}

// The shell bar is the screen's filter, so a navigation it starts must not
// pull the caret out of it. The router used to send focus to #main after every
// mount: a query typed slower than the filter debounce kept only its first
// character, the rest went to the screen, and a background repaint was then
// free to replace the screen under the caret.
test("a scoped query keeps the caret in the shell bar while it filters", async ({
  app,
}) => {
  await mockCompanies(app.page);
  await app.goto("/companies");
  await app.expectRoute("companies");

  const input = app.page.locator("#global-search-input");
  await input.click();
  // Slower than the filter debounce, so every character settles a navigation.
  await input.pressSequentially("Beta", { delay: 350 });

  await expect(input).toHaveValue("Beta");
  await expect(input).toBeFocused();
  await expect(app.page).toHaveURL(/q=Beta/);
  await expect(
    app.page.locator(".companies-row").filter({ hasText: "Beta" }),
  ).toBeVisible();
  await expect(
    app.page.locator(".companies-row").filter({ hasText: "Acme" }),
  ).toBeHidden();
});

test("Companies keeps its placeholder, accessible name and filter across a cached Back", async ({
  app,
}) => {
  await mockCompanies(app.page);
  await app.goto("/companies");
  await app.expectRoute("companies");

  const input = app.page.locator("#global-search-input");
  await expect(input).toHaveAttribute("placeholder", COMPANIES_PLACEHOLDER);
  await expect(input).toHaveAttribute("aria-label", COMPANIES_ARIA);

  await input.fill("Beta");
  const acme = app.page.locator(".companies-row").filter({ hasText: "Acme" });
  const beta = app.page.locator(".companies-row").filter({ hasText: "Beta" });
  await expect(acme).toBeHidden();
  await expect(beta).toBeVisible();
  // The screen's server-side query owns the route identity; wait for it so
  // the Back below lands on the cached screen the owner actually left.
  await expect(app.page).toHaveURL(/q=Beta/);
  await app.page.evaluate(() => {
    (
      document.querySelector(".companies-screen") as HTMLElement
    ).dataset.paintProbe = "before";
  });

  await app.page.locator('[data-nav="settings"]').click();
  await app.expectRoute("settings");
  await expect(input).toHaveAttribute("placeholder", SETTINGS_PLACEHOLDER);
  await expect(input).toHaveAttribute("aria-label", SETTINGS_ARIA);

  await app.page.goBack();
  await app.expectRoute("companies");
  // The same node proves this was the cached restore, not a fresh render.
  await expect(app.page.locator(".companies-screen")).toHaveAttribute(
    "data-paint-probe",
    "before",
  );
  await expect(input).toHaveAttribute("placeholder", COMPANIES_PLACEHOLDER);
  await expect(input).toHaveAttribute("aria-label", COMPANIES_ARIA);
  await expect(input).toHaveValue("Beta");
  await expect(acme).toBeHidden();
  await expect(beta).toBeVisible();
  await expectOneVisibleSearchInput(app);

  const clear = app.page.locator("#global-search-clear");
  await expect(clear).toBeVisible();
  await clear.click();
  await expect(input).toHaveValue("");
  await expect(acme).toBeVisible();
  await expect(beta).toBeVisible();
  await expect(clear).toBeHidden();

  // The restored binding is still live: typing in the shell bar filters the
  // same mounted list without a second field.
  await input.fill("Acme");
  await expect(acme).toBeVisible();
  await expect(beta).toBeHidden();
  await expect(clear).toBeVisible();
  await clear.click();
  await expect(acme).toBeVisible();
  await expect(beta).toBeVisible();
});

test("Logs restores its component filter in the shell bar across a cached Back", async ({
  app,
}) => {
  // A live log or configuration event would rebuild the node whose identity
  // the cached-restore probe asserts; this test owns the stream.
  await installFakeEventSource(app.page);
  await app.goto("/logs?component=api");
  await app.expectRoute("logs");

  // The unauthorized read is a real api log entry, so the filter has rows to
  // keep or drop regardless of what boot already logged.
  const unauthorized = await app.page.request.get(
    app.server.baseURL + "api/status",
  );
  expect(unauthorized.status()).toBe(401);
  await expect(app.page.locator(".log-row").first()).toBeVisible();

  const input = app.page.locator("#global-search-input");
  await expect(input).toHaveAttribute("placeholder", LOGS_PLACEHOLDER);
  await expect(input).toHaveAttribute("aria-label", LOGS_ARIA);
  await expect(input).toHaveValue("api");
  await expect
    .poll(async () => {
      const components = await app.page
        .locator(".log-component")
        .allInnerTexts();
      return (
        components.length > 0 &&
        components.every((value) => value.toLowerCase().includes("api"))
      );
    })
    .toBe(true);
  await app.page.evaluate(() => {
    (document.querySelector(".logs-screen") as HTMLElement).dataset.paintProbe =
      "before";
  });

  await app.page.locator('[data-nav="mail"]').click();
  await app.expectRoute("mail");
  await app.page.goBack();
  await app.expectRoute("logs");

  // The same node proves this was the cached restore, not a fresh render.
  await expect(app.page.locator(".logs-screen")).toHaveAttribute(
    "data-paint-probe",
    "before",
  );
  await expect(input).toHaveAttribute("placeholder", LOGS_PLACEHOLDER);
  await expect(input).toHaveAttribute("aria-label", LOGS_ARIA);
  await expect(input).toHaveValue("api");
  await expect
    .poll(async () => {
      const components = await app.page
        .locator(".log-component")
        .allInnerTexts();
      return (
        components.length > 0 &&
        components.every((value) => value.toLowerCase().includes("api"))
      );
    })
    .toBe(true);
  await expectOneVisibleSearchInput(app);
});

test("Settings preserves its search and filters the full list across a cached Back", async ({
  app,
}) => {
  // A live log or configuration event would rebuild the node whose identity
  // the cached-restore probe asserts; this test owns the stream.
  await installFakeEventSource(app.page);
  const seen = await mockSettings(app.page);
  await app.goto("/settings");
  await app.expectRoute("settings");
  await expect(app.page.locator(".setting-row")).toHaveCount(simpleKeys.length);

  const input = app.page.locator("#global-search-input");
  await input.fill("qa.temperature");
  const target = app.page
    .locator(".setting-row")
    .filter({ hasText: "qa.temperature" });
  await expect(target).toBeVisible();
  await expect(app.page.locator(".setting-row:visible")).toHaveCount(1);
  await expect.poll(() => seen.fullReads).toBe(1);
  await app.page.evaluate(() => {
    (
      document.querySelector(".settings-screen") as HTMLElement
    ).dataset.paintProbe = "before";
  });

  await app.page.locator('[data-nav="mail"]').click();
  await app.expectRoute("mail");
  await app.page.goBack();
  await app.expectRoute("settings");

  // The same node proves this was the cached restore, not a fresh render.
  await expect(app.page.locator(".settings-screen")).toHaveAttribute(
    "data-paint-probe",
    "before",
  );
  await expect(input).toHaveAttribute("placeholder", SETTINGS_PLACEHOLDER);
  await expect(input).toHaveAttribute("aria-label", SETTINGS_ARIA);
  await expect(input).toHaveValue("qa.temperature");
  await expect(target).toBeVisible();
  await expect(app.page.locator(".setting-row:visible")).toHaveCount(1);
  await expectOneVisibleSearchInput(app);
  // The cached mount kept the full list; a Back must not re-read it.
  expect(seen.fullReads).toBe(1);
});

test("Hub clears the screen's binding and performs a global search", async ({
  app,
}) => {
  await mockSettings(app.page);
  await mockApi(app.page, [
    { method: "GET", path: "/api/search", body: searchView() },
  ]);
  await app.goto("/settings");
  await app.expectRoute("settings");

  const input = app.page.locator("#global-search-input");
  await expect(input).toHaveAttribute("aria-label", SETTINGS_ARIA);
  await app.page.locator('[data-nav="hub"]').click();
  await app.expectRoute("hub");

  await expect(input).toHaveAttribute("placeholder", "Search all mail");
  await expect(input).toHaveAttribute("aria-label", "Search all mail");
  await expect(input).toHaveValue("");

  await input.fill("invoice");
  await input.press("Enter");
  await expect(app.page).toHaveURL(/#\/mail\?mode=search&q=invoice/);
});
