import { test, expect } from "@playwright/test";
import { startServer, stopServer, type Server } from "../fixtures/server";

// The first-run overlay is a boot-time screen, not a route. This spec starts
// its own server with the marker unset so the shared worker server stays in
// the normal shell state for every other spec.
test.describe("first run", () => {
  let server: Server;

  test.beforeAll(async () => {
    server = await startServer({ setupDone: false });
  });

  test.afterAll(async () => {
    await stopServer(server);
  });

  test("opens over the shell, applies a language, and closes on finish", async ({ browser }) => {
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

    await page.goto(server.baseURL);
    await expect(page.locator("#setup-root")).toBeVisible();
    await expect(page.locator("#app")).toBeHidden();
    await expect(page.getByRole("heading", { name: "Set up Mailsense" })).toBeVisible();

    // The welcome step shows what the app already knows.
    await expect(page.getByText("Data directory")).toBeVisible();
    await expect(page.locator("#setup-zone")).toHaveValue("UTC");

    // The choice is saved when Continue is pressed, then applied before the
    // next step paints. The button itself is still in the current language.
    await page.locator("#setup-language").selectOption("fr");
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page.getByRole("heading", { name: "Configurer Mailsense" })).toBeVisible();

    // The mail step is escapable; this server has no live source on Linux.
    await page.getByRole("button", { name: "Pas maintenant" }).click();
    await expect(page.getByText("Moteur local.")).toBeVisible();

    await page.getByRole("button", { name: "Continuer" }).click();
    await expect(page.getByRole("button", { name: "Terminer", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Terminer", exact: true }).click();

    await expect(page.locator("#setup-root")).toHaveCount(0);
    await expect(page.locator("#app")).toBeVisible();

    // The marker is the only completion fact, so a reload does not reopen it.
    await page.reload();
    await expect(page.locator("#app")).toBeVisible();
    await expect(page.locator("#setup-root")).toHaveCount(0);

    await context.close();
  });
});
