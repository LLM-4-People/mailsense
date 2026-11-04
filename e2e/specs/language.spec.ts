import { learningView, messageListPage, messageView, msg, priorityView, questionsView, searchView, triageResult } from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

// Regression: the saved interface language was read into the startup batch
// and then destructured by position, so it was never applied — a French
// setting came back as the navigator's English on every fresh load, and
// only a live change of the setting was honoured.
test("a saved interface language applies to the first paint", async ({ app }) => {
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/settings/ui.language",
      body: { key: "ui.language", value: "fr" },
    },
  ]);
  await app.goto("/");
  await expect(app.page.locator("html")).toHaveAttribute("lang", /^fr/);
  await expect(app.page.locator('nav [data-i18n="nav.settings"]')).toHaveText("Réglages");
});

test("an English setting keeps the interface in English", async ({ app }) => {
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/settings/ui.language",
      body: { key: "ui.language", value: "en" },
    },
  ]);
  await app.goto("/");
  await expect(app.page.locator("html")).toHaveAttribute("lang", /^en/);
  await expect(app.page.locator('nav [data-i18n="nav.settings"]')).toHaveText("Settings");
});

// A live language switch changes the chrome and every t() label, but the
// route identity stays the same. The router must force the mounted body to
// rebuild; keeping the same screen node left Settings and Mail in English.
test("a live language switch rebuilds the mounted body", async ({ app }) => {
  await installFakeEventSource(app.page);
  let language = "en";
  const settingsView = () => ({
    settings: [
      {
        key: "ui.language",
        type: "enum",
        default: "auto",
        enum: ["auto", "en", "fr"],
        group: "Interface",
        description: "Interface language.",
        value: language,
        layer: "file",
        restart_pending: false,
      },
    ],
    restart_pending: [],
  });
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/priority", body: priorityView() },
    { method: "GET", path: "/api/messages", body: messageListPage() },
    {
      method: "GET",
      path: /^\/api\/messages\/\d+$/,
      handler: async ({ route, url }) => {
        await fulfillJson(route, messageView(Number(url.pathname.split("/").pop())));
      },
    },
    { method: "GET", path: "/api/questions", body: questionsView() },
    {
      method: "GET",
      path: "/api/settings/ui.language",
      handler: async ({ route }) => {
        await fulfillJson(route, { key: "ui.language", value: language });
      },
    },
    { method: "GET", path: "/api/settings", handler: async ({ route }) => {
      await fulfillJson(route, settingsView());
    } },
    {
      method: "PUT",
      path: "/api/settings/ui.language",
      handler: async ({ route }) => {
        language = "fr";
        await fulfillJson(route, { key: "ui.language", value: language });
      },
    },
  ]);

  await app.goto("/mail?mode=important");
  await app.expectRoute("mail");
  await expect(app.page.locator(".mail-screen")).toBeVisible();
  await app.page.evaluate(() => {
    (document.querySelector(".mail-screen") as HTMLElement).dataset.paintProbe = "before";
  });

  await app.page.locator('[data-nav="settings"]').click();
  await app.expectRoute("settings");
  await expect(app.page.locator(".settings-screen")).toBeVisible();
  await app.page.evaluate(() => {
    (document.querySelector(".settings-screen") as HTMLElement).dataset.paintProbe = "before";
  });

  const row = app.page.locator(".setting-row").filter({ hasText: "ui.language" });
  await row.locator("select").selectOption("fr");
  await row.getByRole("button", { name: "Apply" }).click();

  await expect(app.page.locator("html")).toHaveAttribute("lang", /^fr/);
  // The mounted Settings node itself was replaced by the forced rebuild.
  await expect(app.page.locator(".settings-screen")).not.toHaveAttribute("data-paint-probe", "before");
  await expect(app.page.locator(".setting-row").filter({ hasText: "ui.language" }))
    .toContainText("La langue de la fenêtre Mailsense");

  await app.page.locator('[data-nav="mail"]').click();
  await app.expectRoute("mail");
  await expect(app.page.locator(".mail-screen")).toBeVisible();
  await expect(app.page.locator(".mail-screen")).not.toHaveAttribute("data-paint-probe", "before");
  await expect(app.page.locator(".mail-tabs")).toContainText("Importants");
});

// The likely answer is one catalog sentence with the value as a placeholder,
// not two translated fragments joined in code ("probable Oui").
test("a French likely answer is one templated sentence", async ({ app }) => {
  const triage = triageResult(msg.invoice);
  triage.values = {
    urgent: { type: "noul", number: 0.7, decided: "yes" },
  };
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/settings/ui.language",
      body: { key: "ui.language", value: "fr" },
    },
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/mailboxes", body: { mailboxes: [] } },
    { method: "GET", path: "/api/search", body: searchView() },
    { method: "GET", path: "/api/messages/101", body: messageView(msg.invoice) },
    { method: "GET", path: "/api/triage/101", body: triage },
    { method: "GET", path: "/api/questions", body: questionsView() },
  ]);

  await app.goto("/mail?mode=search&q=invoice&id=101");
  await app.expectRoute("mail");
  const details = app.page.locator(".triage-model-details");
  await expect(details).toBeVisible();
  await details.locator("summary").click();
  const value = details.locator(".triage-model-v");
  await expect(value).toContainText(/Probable\s*:\s*Oui/);
  await expect(value).not.toContainText("Probable Oui");
});
