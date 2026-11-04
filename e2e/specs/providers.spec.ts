import { fulfillJson, mockApi } from "../fixtures/mock";
import { expect, test } from "../fixtures/test";

// The providers view owns the protocol and role catalog. A stub must carry
// the same shape the server sends, or the editor has nothing to choose from.
const providerCatalog = {
  protocols: [
    { name: "openai_chat", kind: "generator" },
    { name: "openai_embeddings", kind: "embedder" },
    { name: "openai_decisions", kind: "decider" },
    { name: "systemone", kind: "decider" },
  ],
  roles: [
    { setting: "qa.provider", label: "Q&A generator", kind: "generator", provider: "", configured: false },
    { setting: "judge.provider", label: "judge generator", kind: "generator", provider: "", configured: false },
    { setting: "triage.decider", label: "triage decider", kind: "decider", provider: "", configured: false },
    { setting: "embed.provider", label: "semantic embedder", kind: "embedder", provider: "", configured: false },
  ],
};

test("a provider saves with Advanced left closed", async ({ app }) => {
  const providers: Array<Record<string, unknown>> = [];
  let created: Record<string, unknown> | null = null;
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/providers",
      handler: async ({ route }) => {
        await fulfillJson(route, { providers, ...providerCatalog });
      },
    },
    {
      method: "POST",
      path: "/api/providers",
      handler: async ({ route, request }) => {
        created = request.postDataJSON() as Record<string, unknown>;
        const record = { ...created, key_present: false };
        providers.push(record);
        await fulfillJson(route, record);
      },
    },
  ]);

  await app.goto("/settings?tab=providers");
  await app.page.getByRole("button", { name: "Add provider" }).first().click();
  await app.page.getByLabel("Label").fill("Acme Chat");
  await app.page.getByLabel("Server URL").fill("https://api.acme.test/v1");
  // Path is a required everyday field. When it lived in the closed
  // Advanced disclosure, filling it here was impossible and Save reported
  // "Path is required" for an invisible input.
  await app.page.getByLabel("Path").fill("/chat/completions");
  // "Model" also matches the "Official models" discovery select by
  // substring, so name the one field exactly.
  await app.page.getByLabel("Model", { exact: true }).fill("acme-chat");
  const advanced = app.page.locator(".provider-advanced");
  // A <details> without the attribute is closed; read the property so the
  // assertion does not depend on attribute serialization.
  expect(await advanced.evaluate((element) => (element as HTMLDetailsElement).open)).toBe(false);
  await app.page.getByRole("button", { name: "Save provider" }).click();

  await expect.poll(() => created).not.toBeNull();
  expect(created?.path).toBe("/chat/completions");
  expect(created?.id).toBe("acme-chat");
  expect(created?.model).toBe("acme-chat");
  // The roles selects also contain an "Acme Chat" option; assert the card
  // itself so the visibility check cannot resolve to a hidden option.
  await expect(app.page.locator('article[data-provider="acme-chat"]')).toBeVisible();
});

test("a provider load error is the list state, not an empty list", async ({ app }) => {
  let fail = true;
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/providers",
      handler: async ({ route }) => {
        if (fail) {
          await fulfillJson(
            route,
            { error: { code: "internal", message: "synthetic provider failure" } },
            500,
          );
          return;
        }
        await fulfillJson(route, { providers: [], ...providerCatalog });
      },
    },
  ]);

  await app.goto("/settings?tab=providers");
  const banner = app.page.locator(".warning-banner");
  await expect(banner).toContainText("synthetic provider failure");
  // The banner already says the list is unknown; an empty card under it
  // would claim there are no providers.
  await expect(app.page.getByText("No providers yet")).toHaveCount(0);

  fail = false;
  // The retry action is the shared button primitive, so it keeps the
  // keyboard parity of every other action: focusable and Enter activates it.
  const retry = banner.getByRole("button", { name: "Try again" });
  await retry.focus();
  await expect(retry).toBeFocused();
  await retry.press("Enter");

  await expect(app.page.locator(".warning-banner")).toHaveCount(0);
  await expect(app.page.getByText("No providers yet")).toBeVisible();
});

test("a save error reveals Advanced and names the field", async ({ app }) => {
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/providers",
      handler: async ({ route }) => {
        await fulfillJson(route, { providers: [], ...providerCatalog });
      },
    },
    {
      method: "POST",
      path: "/api/providers",
      handler: async ({ route }) => {
        await fulfillJson(
          route,
          { error: { code: "invalid_value", message: "retry.max_attempts must be 1 to 10" } },
          400,
        );
      },
    },
  ]);

  await app.goto("/settings?tab=providers");
  await app.page.getByRole("button", { name: "Add provider" }).first().click();
  await app.page.getByLabel("Label").fill("Acme Chat");
  await app.page.getByLabel("Server URL").fill("https://api.acme.test/v1");
  await app.page.getByLabel("Path").fill("/chat/completions");
  await app.page.getByLabel("Model", { exact: true }).fill("acme-chat");
  const advanced = app.page.locator(".provider-advanced");
  expect(await advanced.evaluate((element) => (element as HTMLDetailsElement).open)).toBe(false);

  await app.page.getByRole("button", { name: "Save provider" }).click();
  // The message names a field behind the disclosure; leaving it closed would
  // hide both the error and the input the owner has to change.
  await expect(app.page.getByText("retry.max_attempts must be 1 to 10")).toBeVisible();
  await expect
    .poll(() => advanced.evaluate((element) => (element as HTMLDetailsElement).open))
    .toBe(true);
});

// A provider test reports a human duration, not a bare millisecond count.
test("a provider test reports its duration through the shared formatter", async ({ app }) => {
  const record = {
    id: "acme-chat",
    label: "Acme Chat",
    protocol: "openai_chat",
    kind: "generator",
    model: "acme-chat",
    server_url: "https://api.acme.test/v1",
    path: "/chat/completions",
    key_present: false,
  };
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/providers",
      body: { providers: [record], ...providerCatalog },
    },
    {
      method: "POST",
      path: "/api/providers/acme-chat/test",
      body: { ok: true, model: "acme-chat", latency_ms: 900, detail: "generator_answered" },
    },
  ]);

  await app.goto("/settings?tab=providers");
  const card = app.page.locator('article[data-provider="acme-chat"]');
  await expect(card).toBeVisible();
  await card.getByRole("button", { name: "Test" }).click();
  await app.page.getByRole("button", { name: "Run paid test" }).click();

  await expect(card.locator(".provider-result")).toContainText("Passed in 900 ms");
});
