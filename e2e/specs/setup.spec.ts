import { test, expect, type Browser, type BrowserContext, type Page } from "@playwright/test";
import { startServer, stopServer, type Server } from "../fixtures/server";

// The first-run overlay is a boot-time screen, not a route. Every test starts
// its own server with the marker unset so the shared worker server stays in
// the normal shell state for every other spec.
async function openFirstRun(
  browser: Browser,
  server: Server,
  beforeGoto?: (page: Page) => Promise<void>,
): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({ locale: "en-US", timezoneId: "UTC" });
  const page = await context.newPage();
  await page.addInitScript(
    ({ token }) => {
      try {
        sessionStorage.setItem("mailsense.token", token);
      } catch {
        // An opaque origin cannot use storage; the real launcher does not
        // hit this path either.
      }
    },
    { token: server.token },
  );
  // The setup overlay pre-fetches as soon as it opens, so a route that shapes
  // a pre-fetch must exist before the first navigation.
  if (beforeGoto) await beforeGoto(page);
  await page.goto(server.baseURL);
  await expect(page.locator("#setup-root")).toBeVisible();
  await expect(page.locator("#app")).toBeHidden();
  return { context, page };
}

test.describe("first run", () => {
  test("keeps the basic path short, resumes a reload, and writes the marker once", async ({ browser }) => {
    const server = await startServer({ setupDone: false });
    try {
      // The overlay uses one write of the completion marker. Counting the PUT
      // is the honest check of "exactly once", including a double press.
      let markerWrites = 0;
      const { context, page } = await openFirstRun(browser, server, async (page) => {
        await page.route("**/api/settings/ui.setup.done", async (route) => {
          if (route.request().method() === "PUT") markerWrites += 1;
          await route.continue();
        });
      });

      await expect(page.getByRole("heading", { name: "Set up Mailsense" })).toBeVisible();
      await expect(page.getByRole("dialog")).toBeVisible();
      await expect(page.locator("#setup-step-title")).toBeFocused();
      await expect(page.locator("#setup-step-title")).toHaveText("Welcome");
      await expect(page.getByText("Data directory")).toBeVisible();
      await expect(page.locator("#setup-zone")).toHaveValue("UTC");
      await expect(page.getByRole("button", { name: "Basic" })).toHaveAttribute("aria-pressed", "true");
      await expect(page.locator(".setup-advanced")).toHaveCount(0);

      // The dialog owns the tab order.
      await page.keyboard.press("Tab");
      expect(await page.evaluate(() => document.getElementById("setup-root")?.contains(document.activeElement))).toBe(true);

      // Escape closes only while no write is in flight, and it keeps the
      // progress so a reload returns to the same step.
      await page.keyboard.press("Escape");
      await expect(page.locator("#setup-root")).toHaveCount(0);
      await expect(page.locator("#app")).toBeVisible();
      await page.reload();
      await expect(page.locator("#setup-root")).toBeVisible();
      await expect(page.locator("#setup-step-title")).toHaveText("Welcome");

      // Welcome commits the prefilled zone, then the mailbox step explains
      // that this build has no live source and lets the owner move on.
      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.locator("#setup-step-title")).toHaveText("Mail");
      await expect(page.getByText("This build has no live mail source.").first()).toBeVisible();
      await page.getByRole("button", { name: "Not now", exact: true }).click();
      await expect(page.locator("#setup-step-title")).toHaveText("Model");
      await expect(page.getByText("Mail was skipped.")).toBeVisible();
      await expect(page.getByRole("button", { name: "Open Sources" })).toBeVisible();

      // The model step is completed; a reload does not repeat the earlier
      // steps.
      await page.reload();
      await expect(page.locator("#setup-root")).toBeVisible();
      await expect(page.locator("#setup-step-title")).toHaveText("Model");

      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.locator("#setup-step-title")).toHaveText("Finish");
      await expect(page.getByText("First sync:")).toBeVisible();

      // Not now is honest: it names what stays unavailable and links to the
      // exact screen that fixes each item before the overlay closes.
      await page.getByRole("button", { name: "Finish for now" }).click();
      await expect(page.getByRole("heading", { name: "What stays unavailable" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Open Sources" })).toBeVisible();
      await page.getByRole("button", { name: "Close setup" }).click();

      await expect(page.locator("#setup-root")).toHaveCount(0);
      await expect(page.locator("#app")).toBeVisible();
      expect(markerWrites).toBe(1);

      // The marker is the only completion fact, so a reload does not reopen
      // the overlay.
      await page.reload();
      await expect(page.locator("#app")).toBeVisible();
      await expect(page.locator("#setup-root")).toHaveCount(0);

      await context.close();
    } finally {
      await stopServer(server);
    }
  });

  test("does not offer a source that cannot run", async ({ browser }) => {
    const server = await startServer({ setupDone: false });
    try {
      const { context, page } = await openFirstRun(browser, server, async (page) => {
        await page.route("**/api/sources", async (route) => {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              sources: [{
                id: "outlook",
                kind: "outlook",
                display_name: "Work Outlook",
                capabilities: [],
                actions: ["start"],
                available: false,
                cause: "outlook_not_running",
                detail: "classic Outlook is not running",
              }],
            }),
          });
        });
      });

      await page.getByRole("button", { name: "Continue" }).click();
      await expect(page.locator("#setup-step-title")).toHaveText("Mail");
      await expect(page.locator("#setup-source")).toBeDisabled();
      await expect(page.getByText(/Work Outlook cannot be used/)).toBeVisible();
      await expect(page.getByText(/Classic Outlook is not running/)).toBeVisible();
      await expect(page.getByRole("button", { name: "Look for stores" })).toBeDisabled();
      await expect(page.getByRole("button", { name: "Start Work Outlook" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Check again" })).toBeVisible();

      await context.close();
    } finally {
      await stopServer(server);
    }
  });

  test("advanced shows provider, runtime, storage and network facts without leaving setup", async ({ browser }) => {
    const server = await startServer({ setupDone: false });
    try {
      const { context, page } = await openFirstRun(browser, server, async (page) => {
        // The effective proxy is environment-owned here. The overlay must say
        // which variable wins instead of showing the file value as editable.
        await page.route("**/api/settings**", async (route) => {
          if (route.request().method() !== "GET") {
            await route.continue();
            return;
          }
          const response = await route.fetch();
          let body: { settings?: Array<Record<string, unknown>> };
          try {
            body = (await response.json()) as typeof body;
          } catch {
            await route.fulfill({ response });
            return;
          }
          for (const setting of body.settings ?? []) {
            if (setting.key === "http.proxy_url") {
              setting.layer = "env";
              setting.value = "http://proxy.example.test:8080";
            }
          }
          await route.fulfill({ response, json: body });
        });
      });

      await page.getByRole("button", { name: "Advanced" }).click();
      await expect(page.getByRole("heading", { name: "Advanced setup" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Advanced" })).toHaveAttribute("aria-pressed", "true");

      // Providers: role status and a test action, without a second editor.
      const providers = page.getByRole("region", { name: "Providers" });
      await expect(providers).toBeVisible();
      await expect(providers.getByText("Embeddings", { exact: true }).first()).toBeVisible();
      await expect(providers.getByRole("button", { name: "Test" }).first().or(providers.getByText("No provider is configured yet."))).toBeVisible();
      await expect(page.getByRole("button", { name: "Open Providers in Settings" })).toBeVisible();

      // Runtime: the group names the engine state and always links to the
      // screen that owns the install.
      const runtime = page.getByRole("region", { name: "Local runtime" });
      await expect(runtime).toBeVisible();
      await expect(runtime.getByRole("button", { name: "Open Runtime in Settings" })
        .or(runtime.getByRole("button", { name: "Open Settings, Providers" }))).toBeVisible();

      // Storage and privacy: the facts that matter before the first sync,
      // read from the same schema rows as Settings.
      await expect(page.getByRole("heading", { name: "Storage and privacy" })).toBeVisible();
      await expect(page.getByText("sync.removal.auto_apply_after", { exact: true })).toBeVisible();
      await expect(page.getByText("log.file.max_files", { exact: true })).toBeVisible();
      await expect(page.locator(".setup-advanced").getByText("Database size")).toBeVisible();

      // Network: proxy and timeout, with the environment override named.
      await expect(page.getByRole("heading", { name: "Network" })).toBeVisible();
      await expect(page.getByText("http.proxy_url", { exact: true })).toBeVisible();
      await expect(page.getByText("http.timeout.total", { exact: true })).toBeVisible();
      await expect(page.getByText("MAILSENSE_HTTP_PROXY_URL")).toBeVisible();

      // Back to basic removes the expansion, so the calm path stays calm.
      await page.getByRole("button", { name: "Basic" }).click();
      await expect(page.locator(".setup-advanced")).toHaveCount(0);

      await context.close();
    } finally {
      await stopServer(server);
    }
  });

  test("a failed pre-fetch explains itself, keeps the other facts, and retries", async ({ browser }) => {
    const server = await startServer({ setupDone: false });
    try {
      let providerReads = 0;
      const { context, page } = await openFirstRun(browser, server, async (page) => {
        await page.route("**/api/providers", async (route) => {
          if (route.request().method() !== "GET") {
            await route.continue();
            return;
          }
          providerReads += 1;
          if (providerReads === 1) {
            await route.fulfill({
              status: 500,
              contentType: "application/json",
              body: JSON.stringify({ error: { code: "internal", message: "providers unavailable" } }),
            });
            return;
          }
          await route.continue();
        });
      });

      await page.getByRole("button", { name: "Advanced" }).click();
      await expect(page.getByText("Providers could not be read.")).toBeVisible();

      // One failed read does not block the facts that did land.
      await expect(page.getByText("log.file.max_files", { exact: true })).toBeVisible();
      await expect(page.getByText("http.proxy_url", { exact: true })).toBeVisible();

      await page.getByRole("button", { name: "Try again" }).first().click();
      await expect(page.getByText("Providers could not be read.")).toHaveCount(0);
      await expect(page.getByRole("region", { name: "Providers" }).getByRole("button", { name: "Test" }).first()).toBeVisible();

      await context.close();
    } finally {
      await stopServer(server);
    }
  });
});
