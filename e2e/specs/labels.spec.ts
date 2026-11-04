import { learningView, messageListPage, messageView, priorityView, questionsView } from "../fixtures/data";
import { fulfillJson, mockApi, type Mock } from "../fixtures/mock";
import { installFakeEventSource } from "../fixtures/sse";
import { deferred, expect, hoverNav, test } from "../fixtures/test";

const labelQuestion = {
  name: "category",
  type: "choice",
  instructions: "Pick the category that best describes the message.",
  options: [
    { name: "work", description: "A work update." },
    { name: "action", description: "An action is requested." },
    { name: "finance", description: "A money matter." },
  ],
};

const labelSummary = {
  manual: 0,
  judge: 4,
  needed: 0,
  review: 4,
  questions: [{ question: "category", manual: 0, judge: 4, needed: 0, review: 4 }],
};

const labelTasks = [1001, 1002, 1003, 1004].map((messageId) => ({
  message_id: messageId,
  question: "category",
  reason: "uncertain",
  suggestion: "work",
  suggestion_source: "judge",
}));

// labelMocks is the one read fixture for the Teach queue. Only the label
// write varies: a test can hold it open to observe the busy state or fail it.
// `questions` widens the switcher for the focus and scroll cases; the queue
// answers only the default category question.
function labelMocks(
  onSave: NonNullable<Mock["handler"]>,
  questions = [labelQuestion],
): Mock[] {
  return [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/hub",
      // The hub gate is the one label-summary source when it exists; carry
      // the same totals and question rows the screen would otherwise fetch.
      body: {
        gate: {
          labels: { manual: labelSummary.manual, judge: labelSummary.judge, needed: labelSummary.needed, review: labelSummary.review },
          questions: labelSummary.questions,
        },
      },
    },
    { method: "GET", path: "/api/priority", body: priorityView() },
    { method: "GET", path: "/api/messages", body: messageListPage() },
    {
      method: "GET",
      path: /^\/api\/messages\/\d+$/,
      handler: async ({ route, url }) => {
        await fulfillJson(route, messageView(Number(url.pathname.split("/").pop())));
      },
    },
    {
      method: "GET",
      path: "/api/questions",
      body: { ...questionsView(), questions },
    },
    { method: "GET", path: "/api/labels/summary", body: labelSummary },
    {
      method: "GET",
      path: "/api/labels/queue",
      handler: async ({ route, url }) => {
        const question = url.searchParams.get("question") || "category";
        const tasks = question === "category" ? labelTasks : [];
        await fulfillJson(route, {
          question,
          strategy: "uncertainty",
          tasks,
          total: tasks.length,
          has_more: false,
        });
      },
    },
    { method: "POST", path: "/api/labels", handler: onSave },
  ];
}

// deferred is a one-shot gate: the route handler waits on it until the test
// has observed the in-flight state and released the response.
// Leaving Teach disconnects its document key handler and both observers.
// Re-appending the cached node restores the markup only; without the attach
// hook the shortcuts are dead until a full rebuild.
test("a cached Teach screen keeps its keyboard shortcuts", async ({ app }) => {
  await installFakeEventSource(app.page);
  let writes = 0;
  await mockApi(app.page, labelMocks(async ({ route, request }) => {
    writes += 1;
    const body = request.postDataJSON() as { items?: unknown[] };
    await fulfillJson(route, { stored: body.items || [] });
  }));

  await app.goto("/labels?review=1&question=category");
  await app.expectRoute("labels");
  await expect(app.page.locator(".labels-task-layout")).toBeVisible();
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");

  // Answer without saving. The 1 shortcut must remain an observable change
  // after the cached restore, so leave option 1 unselected here.
  await app.page.keyboard.press("2");
  await expect(app.page.locator('input[name="answer-category"][value="action"]')).toBeChecked();
  await app.page.evaluate(() => {
    (document.querySelector(".labels-screen") as HTMLElement).dataset.paintProbe = "before";
  });

  await app.page.locator('[data-nav="mail"]').click();
  await app.expectRoute("mail");
  await app.page.goBack();
  await app.expectRoute("labels");
  await expect(app.page.locator(".labels-task-layout")).toBeVisible();
  // The same node proves this was the cached restore, not a fresh rebuild.
  await expect(app.page.locator(".labels-screen")).toHaveAttribute("data-paint-probe", "before");

  await app.page.keyboard.press("1");
  await expect(app.page.locator('input[name="answer-category"][value="work"]')).toBeChecked();

  const saved = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/labels",
  );
  await app.page.keyboard.press("Enter");
  await saved;
  // waitForRequest resolves when the request is sent; the mocked handler that
  // counts it runs afterwards, so poll the count instead of racing it.
  await expect.poll(() => writes).toBe(1);
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1002");

  const suggestion = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/labels",
  );
  await app.page.keyboard.press("g");
  await suggestion;
  await expect.poll(() => writes).toBe(2);
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1003");

  const shortcuts = app.page.locator(".labels-shortcuts");
  await expect(shortcuts).not.toHaveAttribute("open", "");
  await app.page.keyboard.press("?");
  await expect(shortcuts).toHaveAttribute("open", "");
});

// A prefetch renders a destination with the live state. Teach keeps its
// answers and task sequence in module state, so hovering the nav link must
// not build a second Teach screen: the mounted task, its selected answer and
// its action bar stay exactly as the owner left them, and Save still writes.
test("hovering a nav destination keeps the mounted task and its answer", async ({ app }) => {
  await installFakeEventSource(app.page);
  await mockApi(app.page, labelMocks(async ({ route, request }) => {
    const body = request.postDataJSON() as { items?: unknown[] };
    await fulfillJson(route, { stored: body.items || [] });
  }));

  await app.goto("/labels?review=1&question=category");
  await app.expectRoute("labels");
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");
  await app.page.keyboard.press("2");
  const action = app.page.locator('input[name="answer-category"][value="action"]');
  await expect(action).toBeChecked();
  const bar = app.page.locator(".labels-actions");
  await expect(bar).toHaveAttribute("aria-busy", "false");
  await expect(app.page.locator(".labels-action-row button").first()).toBeEnabled();

  // The current route has a question and a review parameter; the bare nav
  // href is a different cache key, so this is the prefetch that used to reset
  // state.labelAnswers/labelExtras and bump state.labelTaskSeq.
  await hoverNav(app.page, "labels");
  await hoverNav(app.page, "hub");

  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");
  await expect(action).toBeChecked();
  await expect(bar).toHaveAttribute("aria-busy", "false");
  await expect(app.page.locator(".labels-action-row button").first()).toBeEnabled();

  const saved = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/labels",
    { timeout: 5_000 },
  );
  await app.page.locator(".labels-action-row button").first().click();
  const posted = await saved;
  expect(posted.postDataJSON()).toEqual({
    items: [{ message_id: 1001, question: "category", value: "action" }],
  });
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1002");
});

// The first task's message read is still in flight. Hovering Teach must not
// bump state.labelTaskSeq: the response would then fail its stale guard and
// the mounted workbench would stay on its loading placeholders forever.
test("hovering Teach does not cancel the first task load", async ({ app }) => {
  await installFakeEventSource(app.page);
  const gate = deferred();
  await mockApi(app.page, [
    {
      method: "GET",
      path: /^\/api\/messages\/1001$/,
      query: { for: "label" },
      handler: async ({ route }) => {
        await gate.promise;
        await fulfillJson(route, messageView(1001));
      },
    },
    ...labelMocks(async ({ route, request }) => {
      const body = request.postDataJSON() as { items?: unknown[] };
      await fulfillJson(route, { stored: body.items || [] });
    }),
  ]);

  const read = app.page.waitForRequest(
    (request) => request.method() === "GET" &&
      new URL(request.url()).pathname === "/api/messages/1001" &&
      new URL(request.url()).searchParams.get("for") === "label",
    { timeout: 5_000 },
  );
  await app.goto("/labels?review=1&question=category");
  await app.expectRoute("labels");
  await read;
  await expect(app.page.locator(".labels-task-layout .loading").first()).toBeVisible();

  try {
    await hoverNav(app.page, "labels");
  } finally {
    gate.resolve();
  }

  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");
  await expect(app.page.locator(".labels-task-layout .loading")).toHaveCount(0);
  await expect(app.page.locator('input[name="answer-category"]').first()).toBeVisible();
});

// A save owns the whole task action bar for its request's lifetime. Navigation
// stays inert even if a click is dispatched around the disabled attribute, so
// the saved task's stale index cannot re-render over the task the owner moved
// to. The question controls themselves stay usable on the same task.
test("a label save locks the whole task action bar", async ({ app }) => {
  await installFakeEventSource(app.page);
  const gate = deferred();
  let writes = 0;
  await mockApi(app.page, labelMocks(async ({ route, request }) => {
    writes += 1;
    await gate.promise;
    const body = request.postDataJSON() as { items?: unknown[] };
    await fulfillJson(route, { stored: body.items || [] });
  }));

  await app.goto("/labels?review=1&question=category");
  await app.expectRoute("labels");
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");

  // The middle task has both Previous and Next available before the save.
  await app.page.getByRole("button", { name: "Next" }).click();
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1002");

  // The busy trigger's own label changes while the save is in flight, so the
  // save control is addressed by its stable position in the action row.
  const save = app.page.locator(".labels-action-row button").first();
  const accept = app.page.getByRole("button", { name: "Use suggestion" });
  const skip = app.page.getByRole("button", { name: "Skip" });
  const previous = app.page.getByRole("button", { name: "Previous" });
  const next = app.page.getByRole("button", { name: "Next" });
  const bar = app.page.locator(".labels-actions");
  await expect(bar).toHaveAttribute("aria-busy", "false");

  const posted = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/labels",
  );
  await save.click();
  await posted;

  await expect(bar).toHaveAttribute("aria-busy", "true");
  for (const action of [save, accept, skip, previous, next]) {
    await expect(action).toBeDisabled();
    await expect(action).toHaveAttribute("aria-disabled", "true");
  }

  // A programmatic activation must not move either: the guard lives in the
  // handler, not only in the disabled attribute.
  await skip.dispatchEvent("click");
  await next.dispatchEvent("click");
  await previous.dispatchEvent("click");
  await app.page.waitForTimeout(100);
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1002");

  gate.resolve();
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1003");
  await expect(bar).toHaveAttribute("aria-busy", "false");
  for (const action of [save, accept, skip, previous, next]) {
    await expect(action).toBeEnabled();
  }
  expect(writes).toBe(1);

  // The post-save advance must not re-render over an answer given on the task
  // that replaced the saved one.
  await app.page.keyboard.press("2");
  const action = app.page.locator('input[name="answer-category"][value="action"]');
  await expect(action).toBeChecked();
  await app.page.waitForTimeout(250);
  await expect(action).toBeChecked();
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1003");
});

// A failed save keeps the mounted task and the answer the owner gave. The bar
// becomes usable again and reportError remains the one visible failure.
test("a failed label save re-enables the bar and keeps the answers", async ({ app }) => {
  await installFakeEventSource(app.page);
  const gate = deferred();
  await mockApi(app.page, labelMocks(async ({ route }) => {
    await gate.promise;
    await fulfillJson(route, { error: { code: "internal", message: "synthetic save failure" } }, 500);
  }));

  await app.goto("/labels?review=1&question=category");
  await app.expectRoute("labels");
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");
  await app.page.keyboard.press("2");
  const action = app.page.locator('input[name="answer-category"][value="action"]');
  await expect(action).toBeChecked();

  const save = app.page.locator(".labels-action-row button").first();
  const accept = app.page.getByRole("button", { name: "Use suggestion" });
  const skip = app.page.getByRole("button", { name: "Skip" });
  const previous = app.page.getByRole("button", { name: "Previous" });
  const next = app.page.getByRole("button", { name: "Next" });
  const bar = app.page.locator(".labels-actions");
  const posted = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/labels",
  );
  await save.click();
  await posted;
  await expect(bar).toHaveAttribute("aria-busy", "true");
  await expect(skip).toBeDisabled();

  gate.resolve();
  await expect(app.page.locator("#toasts .toast")).toContainText("synthetic save failure");
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");
  await expect(bar).toHaveAttribute("aria-busy", "false");
  for (const control of [save, accept, skip, next]) {
    await expect(control).toBeEnabled();
  }
  // The first task still has no previous task after the failure.
  await expect(previous).toBeDisabled();
  await expect(action).toBeChecked();
});

// The keyboard shortcuts go through the same guard: skip and save do nothing
// while the save is in flight, then both work once it settles.
test("label keyboard navigation waits for the save to settle", async ({ app }) => {
  await installFakeEventSource(app.page);
  const gate = deferred();
  let writes = 0;
  await mockApi(app.page, labelMocks(async ({ route, request }) => {
    writes += 1;
    await gate.promise;
    const body = request.postDataJSON() as { items?: unknown[] };
    await fulfillJson(route, { stored: body.items || [] });
  }));

  await app.goto("/labels?review=1&question=category");
  await app.expectRoute("labels");
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");

  const posted = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/labels",
  );
  await app.page.keyboard.press("Enter");
  await posted;
  await expect(app.page.locator(".labels-actions")).toHaveAttribute("aria-busy", "true");

  await app.page.keyboard.press("s");
  await app.page.waitForTimeout(100);
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");
  await app.page.keyboard.press("Enter");
  await app.page.waitForTimeout(100);
  expect(writes).toBe(1);

  gate.resolve();
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1002");
  await expect(app.page.locator(".labels-actions")).toHaveAttribute("aria-busy", "false");

  await app.page.keyboard.press("s");
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1003");
  const saved = app.page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/labels",
  );
  await app.page.keyboard.press("Enter");
  await saved;
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1004");
  expect(writes).toBe(2);
});

// A failed /api/gate refresh is permission data, not a Teach read: the queue
// and its task still paint; only the permission pills are withheld.
test("a failed gate refresh does not hide the Teach queue", async ({ app }) => {
  await installFakeEventSource(app.page);
  const mocks = labelMocks(async ({ route }) => {
    await fulfillJson(route, {}, 500);
  });
  mocks.push({
    method: "GET",
    path: "/api/gate",
    handler: async ({ route }) => {
      await fulfillJson(route, { error: { code: "internal", message: "synthetic gate failure" } }, 500);
    },
  });
  await mockApi(app.page, mocks);

  await app.goto("/labels?review=1&question=category");
  await app.expectRoute("labels");
  await expect(app.page.locator(".labels-screen")).toBeVisible();
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");
  await expect(app.page.locator(".labels-pills")).not.toContainText("Gate");
});

// The summary only feeds counts. When it fails with no hub fallback the
// screen keeps its chips, queue and task, says the counts are unavailable,
// and never paints them as 0 / 0. Retry re-reads the route.
test("a failed label summary says so and never paints 0 / 0", async ({ app }) => {
  await installFakeEventSource(app.page);
  let summaryFail = true;
  const recovered = {
    manual: 2,
    judge: 0,
    needed: 5,
    review: 0,
    questions: [{ question: "category", manual: 2, judge: 0, needed: 5, review: 0 }],
  };
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    // No gate snapshot: the summary read is the only count source here.
    { method: "GET", path: "/api/hub", body: {} },
    { method: "GET", path: "/api/priority", body: priorityView() },
    { method: "GET", path: "/api/messages", body: messageListPage() },
    {
      method: "GET",
      path: /^\/api\/messages\/\d+$/,
      handler: async ({ route, url }) => {
        await fulfillJson(route, messageView(Number(url.pathname.split("/").pop())));
      },
    },
    {
      method: "GET",
      path: "/api/questions",
      body: { ...questionsView(), questions: [labelQuestion] },
    },
    {
      method: "GET",
      path: "/api/labels/summary",
      handler: async ({ route }) => {
        if (summaryFail) {
          await fulfillJson(route, { error: { code: "internal", message: "synthetic summary failure" } }, 500);
          return;
        }
        await fulfillJson(route, recovered);
      },
    },
    {
      method: "GET",
      path: "/api/labels/queue",
      body: {
        question: "category",
        strategy: "uncertainty",
        tasks: labelTasks,
        total: labelTasks.length,
        has_more: false,
      },
    },
  ]);

  await app.goto("/labels?review=1&question=category");
  await app.expectRoute("labels");
  // The queue and task are usable: only the counts are unknown.
  await expect(app.page.locator(".labels-task-subject")).toContainText("Synthetic message 1001");

  const errorSlot = app.page.locator(".labels-toolbar .inline-read-error");
  await expect(errorSlot).toContainText("Label progress is unavailable.");
  await expect(errorSlot).toBeVisible();
  await expect(app.page.locator(".labels-screen")).not.toContainText("0 / 0");
  await expect(app.page.locator(".labels-stat strong").first()).toHaveText("Not set");
  await expect(app.page.locator("#topbar-meta")).not.toContainText("Your answers");

  summaryFail = false;
  await errorSlot.getByRole("button", { name: "Try again" }).click();

  await expect(app.page.locator(".labels-toolbar .inline-read-error")).toHaveCount(0);
  await expect(app.page.locator(".labels-qchip-meta").first()).toContainText("2 / 5");
  await expect(app.page.locator("#topbar-meta")).toContainText("Your answers 2 / 5");
});

// The question switcher is a row of links that the screen rebuilds. Each
// chip owns a stable focus key so keyboard activation lands on the chip that
// is now the active question, not on #main.
test("a question chip keeps focus on the selected chip", async ({ app }) => {
  const urgencyQuestion = {
    name: "urgency",
    type: "choice",
    instructions: "Choose the time pressure the message actually creates.",
    options: [
      { name: "low", description: "No pressure." },
      { name: "high", description: "Immediate." },
    ],
  };
  await mockApi(app.page, labelMocks(async ({ route }) => {
    await fulfillJson(route, { stored: [] });
  }, [labelQuestion, urgencyQuestion]));

  await app.goto("/labels?question=category");
  await app.expectRoute("labels");
  const chips = app.page.locator(".labels-question-chips");
  const urgency = chips.getByRole("link", { name: /How urgent is it/ });
  await expect(urgency).toBeVisible();
  await urgency.focus();
  await app.page.keyboard.press("Enter");
  await expect(app.page).toHaveURL(/question=urgency/);
  await expect(urgency).toHaveAttribute("aria-current", "page");
  await expect(urgency).toBeFocused();
});
