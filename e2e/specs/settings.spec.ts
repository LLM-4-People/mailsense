import { fulfillJson } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { hiddenCount, mockSettings, settingView, simpleKeys } from "../fixtures/settings";
import { expect, hoverNav, test } from "../fixtures/test";

test("the default General load requests the simple projection", async ({ app }) => {
  const seen = await mockSettings(app.page);
  await app.goto("/settings");
  await app.expectRoute("settings");

  // The eight curated rows render from the projected payload.
  await expect(app.page.locator(".setting-row")).toHaveCount(8);
  for (const key of simpleKeys) {
    await expect(app.page.locator(".setting-row").filter({ hasText: key })).toBeVisible();
  }
  await expect(app.page.locator(".settings-simple-note:not([hidden])")).toContainText(`Advanced options are hidden (${hiddenCount}).`);
  expect(seen.simpleQueries).toEqual([simpleKeys.join(",")]);
  expect(seen.fullReads).toBe(0);
});

test("Show all loads the full list once and a second toggle reuses it", async ({ app }) => {
  const seen = await mockSettings(app.page);
  await app.goto("/settings");
  await app.expectRoute("settings");
  await expect(app.page.locator(".setting-row")).toHaveCount(8);

  await app.page.getByRole("button", { name: "Show all settings" }).click();
  const advancedRow = app.page.locator(".setting-row").filter({ hasText: "qa.temperature" });
  await expect(advancedRow).toBeVisible();
  await expect.poll(() => seen.fullReads).toBe(1);

  await app.page.getByRole("button", { name: "Back to simple view" }).click();
  await expect(app.page.locator(".setting-row")).toHaveCount(8);
  // The full list is loaded, but the count sentence still reads the same.
  await expect(app.page.locator(".settings-simple-note:not([hidden])")).toContainText(`Advanced options are hidden (${hiddenCount}).`);
  await app.page.getByRole("button", { name: "Show all settings" }).click();
  await expect(advancedRow).toBeVisible();
  // The full payload is already in hand; toggling never re-requests it.
  expect(seen.fullReads).toBe(1);
});

test("searching loads the full list and finds a non-simple setting", async ({ app }) => {
  const seen = await mockSettings(app.page);
  await app.goto("/settings");
  await app.expectRoute("settings");
  await expect(app.page.locator(".setting-row")).toHaveCount(8);

  await app.page.locator("#global-search-input").fill("qa.temperature");
  const advancedRow = app.page.locator(".setting-row").filter({ hasText: "qa.temperature" });
  await expect(advancedRow).toBeVisible();
  await expect.poll(() => seen.fullReads).toBe(1);
  expect(seen.simpleQueries).toEqual([simpleKeys.join(",")]);
});

// Settings keeps its filter and active tab in module state while rendering.
// Hovering the nav link must not build the default General screen over them:
// the Runtime tab then stops revalidating on a sidecar event, and the next
// tab activation loses the filter query.
test("hovering Settings keeps the filter and the active tab", async ({ app }) => {
  await installFakeEventSource(app.page);
  await mockSettings(app.page, [
    {
      method: "GET",
      path: "/api/providers",
      body: { providers: [], protocols: [], roles: [] },
    },
    {
      method: "GET",
      path: "/api/sidecar",
      body: { builds: [], runtimes: [] },
    },
  ]);
  await app.goto("/settings?tab=general&q=proxy");
  await app.expectRoute("settings");

  const search = app.page.locator("#global-search-input");
  await search.fill("prox");
  await search.fill("proxy");
  const proxyRow = app.page.locator(".setting-row").filter({ hasText: "http.proxy_url" });
  await expect(proxyRow).toBeVisible();
  // Leave the shell search before hovering: a background refresh deferred for
  // a focused input must settle first, or its re-render masks the prefetch.
  await search.blur();
  await app.page.waitForTimeout(600);

  const subnav = app.page.locator(".settings-layout .subnav");
  await subnav.getByRole("button", { name: "Local runtime", exact: true }).click();
  await app.expectRoute("settings");
  await expect(app.page).toHaveURL(/tab=runtime/);
  await expect(app.page).toHaveURL(/q=proxy/);
  const runtimeTab = subnav.getByRole("button", { name: "Local runtime", exact: true });
  await expect(runtimeTab).toHaveAttribute("aria-current", "page");

  // The bare nav href is a different cache key from the filtered Runtime
  // route, so this is the prefetch that used to reset state.settingsTab and
  // state.settingsQuery.
  await hoverNav(app.page, "settings");

  await expect(app.page).toHaveURL(/q=proxy/);
  await expect(runtimeTab).toHaveAttribute("aria-current", "page");

  // sidecar.state refreshes the mounted Runtime tab only while the module
  // state still says runtime. A prefetched General screen must not swallow it.
  await app.page.evaluate(() => {
    (document.querySelector(".settings-screen") as HTMLElement).dataset.paintProbe = "before";
  });
  await emitSse(app.page, "sidecar.state", { status: "running" });
  await expect(app.page.locator('.settings-screen[data-paint-probe="before"]')).toHaveCount(0, { timeout: 3_000 });
  await expect(app.page.locator(".settings-screen").last()).toBeVisible();
  await expect(runtimeTab).toHaveAttribute("aria-current", "page");

  await subnav.getByRole("button", { name: "General", exact: true }).click();
  await expect(app.page).toHaveURL(/tab=general/);
  await expect(app.page).toHaveURL(/q=proxy/);
  await expect(search).toHaveValue("proxy");
  await expect(app.page.locator(".setting-row").filter({ hasText: "http.proxy_url" })).toBeVisible();
});

const resourceGroups: Record<"en" | "fr", {
  group: string;
  showAll: string;
  usageNow: string;
  usageLimits: string;
  rows: Array<[string, string]>;
}> = {
  en: {
    group: "Resources",
    showAll: "Show all settings",
    usageNow: "Now: CPU 15 % · memory 44 % · disk 76 %.",
    usageLimits: "Limits: CPU 80 % · memory 85 % · disk 90 %.",
    rows: [
      ["resource.guard", "Keep this computer below its CPU, memory and disk ceilings. At a ceiling, new background work is refused and running work pauses until use falls back below it. On by default; off removes every ceiling."],
      ["resource.cpu.max_percent", "Highest CPU use, in percent, at which background work may run. At or above it, new work is refused and running work pauses. The default of 80 % leaves room for the rest of the computer; 100 % disables the CPU ceiling."],
      ["resource.ram.max_percent", "Highest memory use, in percent, at which background work may run. At or above it, new work is refused and running work pauses. The default of 85 % leaves room for the rest of the computer; 100 % disables the memory ceiling."],
      ["resource.disk.max_percent", "Highest used space, in percent, on the volume that holds the data directory. At or above it, work that writes mail, index or model data is refused, so the app can never fill the disk. The default of 90 % keeps a safety margin; 100 % disables the disk ceiling."],
      ["resource.sample_interval", "How often CPU, memory and disk use are re-read. A paused job re-checks on this cadence, and the status endpoint never reports an older reading. The default of 2 seconds stays responsive without measuring constantly."],
    ],
  },
  fr: {
    group: "Ressources",
    showAll: "Afficher tous les paramètres",
    usageNow: "Maintenant : processeur 15 % · mémoire 44 % · disque 76 %.",
    usageLimits: "Limites : processeur 80 % · mémoire 85 % · disque 90 %.",
    rows: [
      ["resource.guard", "Maintient cet ordinateur sous ses plafonds de processeur, de mémoire et de disque. Au plafond, le nouveau travail d’arrière-plan est refusé et le travail en cours se met en pause jusqu’au retour sous la limite. Actif par défaut ; désactivé, aucun plafond n’est appliqué."],
      ["resource.cpu.max_percent", "Taux d’utilisation maximal du processeur, en pourcentage, pour le travail d’arrière-plan. À ce niveau ou au-dessus, le nouveau travail est refusé et le travail en cours se met en pause. La valeur par défaut de 80 % laisse de la place au reste de l’ordinateur ; 100 % désactive le plafond du processeur."],
      ["resource.ram.max_percent", "Taux d’utilisation maximal de la mémoire, en pourcentage, pour le travail d’arrière-plan. À ce niveau ou au-dessus, le nouveau travail est refusé et le travail en cours se met en pause. La valeur par défaut de 85 % laisse de la place au reste de l’ordinateur ; 100 % désactive le plafond de mémoire."],
      ["resource.disk.max_percent", "Taux d’occupation maximal, en pourcentage, du volume qui contient le répertoire de données. À ce niveau ou au-dessus, tout travail qui écrit des messages, des index ou des modèles est refusé, pour que l’application ne remplisse jamais le disque. La valeur par défaut de 90 % garde une marge de sécurité ; 100 % désactive le plafond du disque."],
      ["resource.sample_interval", "Fréquence de relecture de l’utilisation du processeur, de la mémoire et du disque. Un travail en pause revérifie à ce rythme, et le statut ne rapporte jamais une mesure plus ancienne. La valeur par défaut de 2 secondes reste réactive sans mesurer en permanence."],
    ],
  },
};

for (const language of ["en", "fr"] as const) {
  test(`the Resources group lists the five ceilings in ${language}`, async ({ app }) => {
    const copy = resourceGroups[language];
    const status = {
      resources: {
        usage: {
          cpu: { percent: 15.2, known: true },
          ram: { percent: 44, known: true },
          disk: { percent: 75.9, known: true },
          sampled_at: "2026-11-03T20:00:00Z",
        },
        limits: { enabled: true, cpu: 80, ram: 85, disk: 90 },
      },
    };
    await mockSettings(app.page, [{ method: "GET", path: "/api/status", body: status }], language);
    await app.goto("/settings");
    await app.expectRoute("settings");
    await expect(app.page.locator("html")).toHaveAttribute("lang", new RegExp(`^${language}`));

    await app.page.getByRole("button", { name: copy.showAll }).click();
    const group = app.page.locator(".setting-group").filter({
      has: app.page.getByRole("heading", { name: copy.group, exact: true }),
    });
    await expect(group).toBeVisible();
    await expect(group.locator(".setting-row")).toHaveCount(copy.rows.length);
    for (const [key, description] of copy.rows) {
      const row = group.locator(".setting-row").filter({ hasText: key });
      await expect(row).toBeVisible();
      await expect(row.locator(".setting-title")).toHaveText(description);
    }
    const usage = group.locator(".setting-usage");
    await expect(usage).toContainText(copy.usageNow);
    await expect(usage).toContainText(copy.usageLimits);
  });
}

test("a paused resource ceiling is named beside the live readout", async ({ app }) => {
  await mockSettings(app.page, [
    {
      method: "GET",
      path: "/api/status",
      body: {
        resources: {
          usage: {
            cpu: { percent: 92, known: true },
            ram: { percent: 44, known: true },
            disk: { percent: 75.9, known: true },
          },
          limits: { enabled: true, cpu: 80, ram: 85, disk: 90 },
          paused: { resource: "cpu", percent: 92, limit: 80, at: "2026-11-03T20:00:00Z" },
        },
      },
    },
  ]);
  await app.goto("/settings");
  await app.expectRoute("settings");
  await app.page.getByRole("button", { name: "Show all settings" }).click();
  const group = app.page.locator(".setting-group").filter({
    has: app.page.getByRole("heading", { name: "Resources", exact: true }),
  });
  await expect(group.locator(".setting-usage-paused")).toContainText("Paused: CPU 92 % (limit 80 %).");
});

test("saving a resource limit refreshes the live readout", async ({ app }) => {
  let cpuLimit = 80;
  const status = () => ({
    resources: {
      usage: {
        cpu: { percent: 15.2, known: true },
        ram: { percent: 44, known: true },
        disk: { percent: 75.9, known: true },
      },
      limits: { enabled: true, cpu: cpuLimit, ram: 85, disk: 90 },
    },
  });
  await mockSettings(app.page, [
    {
      method: "GET",
      path: "/api/status",
      handler: async ({ route }) => { await fulfillJson(route, status()); },
    },
    {
      method: "PUT",
      path: "/api/settings/resource.cpu.max_percent",
      handler: async ({ route, request }) => {
        const body = request.postDataJSON() as { value: number };
        cpuLimit = Number(body.value);
        await fulfillJson(route, {
          ...settingView("resource.cpu.max_percent", "Resources"),
          type: "int",
          value: cpuLimit,
          min: 10,
          max: 100,
        });
      },
    },
  ]);
  await app.goto("/settings");
  await app.expectRoute("settings");
  await app.page.getByRole("button", { name: "Show all settings" }).click();
  const group = app.page.locator(".setting-group").filter({
    has: app.page.getByRole("heading", { name: "Resources", exact: true }),
  });
  const row = group.locator(".setting-row").filter({ hasText: "resource.cpu.max_percent" });
  await expect(group.locator(".setting-usage")).toContainText("Limits: CPU 80 %");
  await row.locator("input[type=number]").fill("70");
  await row.getByRole("button", { name: "Apply" }).click();
  await expect(group.locator(".setting-usage")).toContainText("Limits: CPU 70 %");
});
