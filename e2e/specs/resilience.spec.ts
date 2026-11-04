import { askView, learningView, messageListPage, priorityView, statusView } from "../fixtures/data";
import { fulfillJson, mockApi, type Mock } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

const settingsView = {
  settings: [
    {
      key: "ui.language",
      type: "enum",
      default: "auto",
      enum: ["auto", "en", "fr"],
      group: "Interface",
      description: "Interface language.",
      value: "en",
      layer: "file",
      restart_pending: false,
    },
  ],
  restart_pending: [],
};

// A hung route read must end in the shared error surface, not an aria-busy
// skeleton forever. Time is advanced past the 30 s request bound so the test
// proves the timer, not a slow fixture.
test("a timed-out route read paints an error screen whose Retry recovers", async ({ app }) => {
  await installFakeEventSource(app.page);
  await app.page.clock.install({ time: new Date("2026-11-03T12:00:00Z") });
  let learningCalls = 0;
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/status",
      body: statusView(),
    },
    { method: "GET", path: "/api/priority", body: priorityView() },
    { method: "GET", path: "/api/messages", body: messageListPage() },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    { method: "GET", path: "/api/labels/summary", body: { manual: 0, judge: 0, needed: 0, review: 0, questions: [] } },
    {
      method: "GET",
      path: "/api/learning",
      handler: async ({ route }) => {
        learningCalls += 1;
        if (learningCalls === 1) return; // the cold read never answers
        await fulfillJson(route, learningView({ open: true }));
      },
    },
  ]);

  const bootLearning = app.page.waitForRequest(
    (request) => new URL(request.url()).pathname === "/api/learning",
  );
  await app.goto("/mail?mode=important");
  await bootLearning;
  await app.page.locator('[data-nav="learning"]').click();
  await app.expectRoute("learning");
  await app.page.clock.fastForward(31_000);

  const errorScreen = app.page.locator('#main [data-error-screen="1"]');
  await expect(errorScreen).toBeVisible();
  await expect(errorScreen).toContainText("The request timed out.");
  const retry = errorScreen.getByRole("button", { name: "Try again" });
  await expect(retry).toBeEnabled();

  await retry.click();
  await expect(app.page.locator(".learning-screen")).toBeVisible();
  await expect(app.page.locator('#main [data-error-screen="1"]')).toHaveCount(0);
});

// A failed /api/gate must end in the Brief's retry surface, not a router
// crash: the gate guard is a route root, so it has to be an Element. A route
// node is also cached and remounted, so the retry surface must paint again
// instead of restoring a blank or broken node.
test("an unknown gate paints the Brief retry and survives a remount", async ({ app }) => {
  await installFakeEventSource(app.page);
  const pageErrors: string[] = [];
  app.page.on("pageerror", (error) => pageErrors.push(error.message));
  let gateFails = true;
  const gate: Mock = {
    method: "GET",
    path: "/api/gate",
    handler: async ({ route }) => {
      if (gateFails) {
        await fulfillJson(
          route,
          { error: { code: "internal", message: "synthetic gate failure" } },
          500,
        );
        return;
      }
      await fulfillJson(route, { allowed: true, open: true, preview: false });
    },
  };
  await mockApi(app.page, [
    gate,
    { method: "GET", path: "/api/status", body: statusView() },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    { method: "GET", path: "/api/learning", body: learningView({ open: true, preview: false }) },
    {
      method: "GET",
      path: "/api/brief",
      body: {
        must: { count: 0, items: [] },
        waiting: { count: 0, items: [] },
        changed: { count: 0, items: [], since: "" },
        relationships: { count: 0, items: [] },
        topics: { count: 0, items: [], total: 0 },
      },
    },
  ]);

  await app.goto("/brief");
  await app.expectRoute("brief");
  const heading = app.page.locator("#main").getByRole("heading", { name: "The brief cannot open yet" });
  await expect(heading).toBeVisible();
  await expect(app.page.locator("#main")).toContainText("The gate state could not be read.");
  const retry = app.page.locator("#main").getByRole("button", { name: "Try again" });
  await expect(retry).toBeVisible();

  // Leaving and coming back must paint the retry surface again, not restore a
  // blank node from the cache or crash the router a second time.
  await app.page.locator('.sidebar [data-nav="logs"]').click();
  await app.expectRoute("logs");
  await app.page.goBack();
  await app.expectRoute("brief");
  await expect(heading).toBeVisible();
  await expect(retry).toBeVisible();
  expect(pageErrors).toEqual([]);

  // The retry rereads the gate and repaints the Brief once it is known.
  gateFails = false;
  await retry.click();
  await expect(app.page.locator(".brief-stats .stat")).toHaveCount(4);
  await expect(heading).toHaveCount(0);
  expect(pageErrors).toEqual([]);
});

// Ask generation is a paid call. Its explicit long bound must survive a wait
// far beyond the ordinary read bound.
test("a delayed Ask send completes inside the long bound", async ({ app }) => {
  test.setTimeout(60_000);
  await installFakeEventSource(app.page);
  await app.page.clock.install({ time: new Date("2026-11-03T12:00:00Z") });
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/ask/history", body: { items: [] } },
    { method: "GET", path: "/api/settings/qa.provider", body: { key: "qa.provider", value: "stub" } },
    {
      method: "POST",
      path: "/api/ask",
      handler: async ({ route, request }) => {
        const body = request.postDataJSON() as { question: string };
        await held;
        await fulfillJson(route, askView({ question: body.question, answer: "Long synthetic answer." }));
      },
    },
  ]);

  await app.goto("/ask");
  await app.expectRoute("ask");
  await app.page.fill("#ask-input", "Summarize the synthetic invoice thread");
  const posted = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/ask",
  );
  await app.page.locator(".ask-actions").getByRole("button", { name: "Send" }).click();
  await posted;
  // Past the ordinary 30 s read bound; the paid call's 5 min bound holds.
  await app.page.clock.fastForward(31_000);
  release();

  await expect(app.page.locator(".ask-answer")).toContainText("Long synthetic answer.");
});

// A mutation that times out may still have been applied. The transport maps
// the abort to a mutation timeout, the inline error says "no response yet",
// and the shared reads are re-fetched instead of claiming a failure.
test("a timed-out mutation reports no response and reloads the reads", async ({ app }) => {
  await installFakeEventSource(app.page);
  await app.page.clock.install({ time: new Date("2026-11-03T12:00:00Z") });
  let learningReads = 0;
  await mockApi(app.page, [
    { method: "GET", path: "/api/status", body: statusView() },
    { method: "GET", path: "/api/settings", body: settingsView },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    {
      method: "GET",
      path: "/api/learning",
      handler: async ({ route }) => {
        learningReads += 1;
        await fulfillJson(route, learningView({ open: true }));
      },
    },
    {
      method: "PUT",
      path: "/api/settings/ui.language",
      handler: async () => {
        // The write never answers; its server-side outcome is unknown.
      },
    },
  ]);

  await app.goto("/settings");
  await app.expectRoute("settings");
  const row = app.page.locator(".setting-row").filter({ hasText: "ui.language" });
  await expect(row).toBeVisible();
  learningReads = 0;
  const pending = app.page.waitForRequest(
    (request) => request.method() === "PUT" && new URL(request.url()).pathname === "/api/settings/ui.language",
  );
  await row.getByRole("button", { name: "Apply" }).click();
  await pending;
  await app.page.clock.fastForward(30_500);

  await expect(row.locator(".field-error")).toContainText(
    "The request timed out before the server answered",
  );
  await expect.poll(() => learningReads).toBeGreaterThanOrEqual(1);
  await expect(row.getByRole("button", { name: "Apply" })).toBeEnabled();
});

// An action that already paints its error inline must still emit exactly one
// toast when the top-level runner reports it, never a second copy. The
// learning read is mocked without a machine zone blocker so the first-run
// auto-confirm cannot add its own toast to this surface.
test("an inline-reporting action emits exactly one toast", async ({ app }) => {
  await installFakeEventSource(app.page);
  await mockApi(app.page, [
    { method: "GET", path: "/api/status", body: statusView() },
    { method: "GET", path: "/api/settings", body: settingsView },
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "PUT",
      path: "/api/settings/ui.language",
      handler: async ({ route }) => {
        await fulfillJson(route, { error: { code: "invalid_request", message: "synthetic refusal" } }, 500);
      },
    },
    {
      method: "POST",
      path: "/api/learning/zone",
      handler: async ({ route }) => {
        await fulfillJson(route, {});
      },
    },
  ]);

  await app.goto("/settings");
  await app.expectRoute("settings");
  const row = app.page.locator(".setting-row").filter({ hasText: "ui.language" });
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: "Apply" }).click();

  await expect(row.locator(".field-error")).toContainText("synthetic refusal");
  // Count this action's own surface, not every toast the shell may show.
  const refusal = app.page.locator("#toasts .toast").filter({ hasText: "synthetic refusal" });
  await expect(refusal).toHaveCount(1);
  await expect(row.getByRole("button", { name: "Apply" })).toBeEnabled();
});

// The shell reads behind the visible screen share one error policy. An Ask
// failure paints inside its turn and nowhere else; a background read still
// reports one toast; the expected shutdown reply is silent.

function shellMocks(learning: Mock): Mock[] {
  return [
    { method: "GET", path: "/api/status", body: statusView() },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    { method: "GET", path: "/api/gate", body: { allowed: true, open: true, preview: false } },
    learning,
  ];
}

const askMocks: Mock[] = [
  { method: "GET", path: "/api/ask/history", body: { items: [] } },
  { method: "GET", path: "/api/settings/qa.provider", body: { key: "qa.provider", value: "stub" } },
];

test("an Ask 500 paints one inline card and no toast", async ({ app }) => {
  await installFakeEventSource(app.page);
  await mockApi(app.page, [
    ...shellMocks({ method: "GET", path: "/api/learning", body: learningView({ open: true }) }),
    ...askMocks,
    {
      method: "POST",
      path: "/api/ask",
      handler: async ({ route }) => {
        await fulfillJson(route, { error: { code: "internal", message: "synthetic ask failure" } }, 500);
      },
    },
  ]);

  await app.goto("/ask");
  await app.expectRoute("ask");
  await app.page.fill("#ask-input", "Why did the synthetic answer fail?");
  await app.page.locator(".ask-actions").getByRole("button", { name: "Send" }).click();

  const card = app.page.locator(".ask-answer-error");
  await expect(card).toBeVisible();
  await expect(card).toContainText("synthetic ask failure");
  // Scope the no-toast rule to the Ask failure itself; the shell may show an
  // unrelated notice without contradicting the inline-only surface.
  await expect(
    app.page.locator("#toasts .toast").filter({ hasText: "synthetic ask failure" }),
  ).toHaveCount(0);
});

test("a timed-out Ask send keeps its no-response card and adds no toast", async ({ app }) => {
  test.setTimeout(60_000);
  await installFakeEventSource(app.page);
  await app.page.clock.install({ time: new Date("2026-11-03T12:00:00Z") });
  await mockApi(app.page, [
    ...shellMocks({ method: "GET", path: "/api/learning", body: learningView({ open: true }) }),
    ...askMocks,
    { method: "POST", path: "/api/ask", handler: async () => { /* the server never answers */ } },
  ]);

  await app.goto("/ask");
  await app.expectRoute("ask");
  await app.page.fill("#ask-input", "A question the server never answers");
  const posted = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/ask",
  );
  await app.page.locator(".ask-actions").getByRole("button", { name: "Send" }).click();
  await posted;
  // Past the paid call's 5 min bound; the mutation timeout is the client's own.
  await app.page.clock.fastForward(301_000);

  const card = app.page.locator(".ask-answer-error");
  await expect(card).toContainText("No response yet");
  await expect(card).toContainText("The request timed out before the server answered");
  // The inline card is the one surface for this Ask failure, not a global
  // toast count: unrelated shell notices stay outside this assertion.
  await expect(
    app.page.locator("#toasts .toast").filter({
      hasText: "The request timed out before the server answered",
    }),
  ).toHaveCount(0);
});

test("a failed background learning refresh reports exactly one toast", async ({ app }) => {
  await installFakeEventSource(app.page);
  let learningCalls = 0;
  await mockApi(app.page, shellMocks({
    method: "GET",
    path: "/api/learning",
    handler: async ({ route }) => {
      learningCalls += 1;
      if (learningCalls === 1) {
        await fulfillJson(route, learningView({ open: true }));
        return;
      }
      await fulfillJson(route, { error: { code: "internal", message: "synthetic learning failure" } }, 500);
    },
  }));

  await app.goto("/logs");
  await app.expectRoute("logs");
  await expect(
    app.page.locator("#log-rows table").or(app.page.locator("#log-rows .empty")).first(),
  ).toBeVisible();
  await emitSse(app.page, "index.finished");

  const toast = app.page.locator("#toasts .toast");
  await expect(toast).toHaveCount(1);
  await expect(toast).toContainText("synthetic learning failure");
});

test("a shutting_down reply during shutdown adds no toast", async ({ app }) => {
  await installFakeEventSource(app.page);
  await mockApi(app.page, [
    ...shellMocks({ method: "GET", path: "/api/learning", body: learningView({ open: true }) }),
    {
      method: "POST",
      path: "/api/shutdown",
      handler: async ({ route }) => {
        await fulfillJson(route, { error: { code: "shutting_down", message: "synthetic shutdown" } }, 503);
      },
    },
  ]);

  await app.goto("/logs");
  await app.expectRoute("logs");
  await app.page.locator("#shutdown-button").click();
  await app.page.locator("#modal").getByRole("button", { name: "Shut down" }).click();

  await expect(app.page.locator("#main")).toContainText("Mailsense is shutting down");
  await expect(app.page.locator("#toasts .toast")).toHaveCount(0);
});
