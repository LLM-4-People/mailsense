import {
  hubCertificationView,
  labelsSummaryView,
  learningView,
} from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

test.describe("live progress", () => {
  test("a running index paints its real totals and advances in place", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/learning", body: learningView() },
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
      {
        method: "GET",
        path: "/api/labels/summary",
        body: labelsSummaryView(),
      },
    ]);

    let learningRequests = 0;
    app.page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/learning")
        learningRequests += 1;
    });

    await app.goto("/learning");
    await app.expectRoute("learning");
    await emitSse(app.page, "open");

    const stage = app.page.locator('[data-stage-id="embeddings"]');
    await expect(stage).toContainText(/13,696\s*\/\s*34,938/);
    await expect(stage).toContainText("13,650 embedded");
    await expect(stage).toContainText("46 unchanged");

    const before = learningRequests;
    await emitSse(app.page, "index.progress", {
      done: 20_000,
      total: 34_938,
      embedded: 19_950,
      unchanged: 50,
      failed: 0,
    });
    await expect(stage).toContainText(/20,000\s*\/\s*34,938/);
    await expect(stage).toContainText("19,950 embedded");
    expect(learningRequests).toBe(before);
  });

  test("a running layer-1 stage shows its phase, question and ETA", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/learning",
        body: learningView({
          status: {
            state: "learning",
            scope: "store:e2e",
            at: "2026-11-02T08:00:00Z",
            stages: [
              {
                id: "mailboxes",
                state: "done",
                progress: { done: 1, total: 1, unit: "mailboxes" },
              },
              {
                id: "layer1",
                state: "running",
                progress: {
                  done: 3,
                  total: 17,
                  unit: "questions",
                  stage: "fold",
                  detail: "topic",
                  step_done: 2,
                  step_total: 5,
                },
                eta: 90_000_000_000,
              },
            ],
          },
        }),
      },
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
      {
        method: "GET",
        path: "/api/labels/summary",
        body: labelsSummaryView(),
      },
    ]);

    await app.goto("/learning");
    await app.expectRoute("learning");
    await emitSse(app.page, "open");

    const stage = app.page.locator('[data-stage-id="layer1"]');
    await expect(stage).toContainText("Cross-validating");
    await expect(stage).toContainText("What is it about?");
    // The timeline shows the same step position as the job chip.
    await expect(stage).toContainText("step 2 / 5");
    await expect(stage).toContainText("about 1.5 min left");
  });

  test("jobs.update forces a fresh status and re-renders the visible route", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/learning", body: learningView() },
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
      {
        method: "GET",
        path: "/api/labels/summary",
        body: labelsSummaryView(),
      },
    ]);

    await app.goto("/learning");
    await app.expectRoute("learning");
    await emitSse(app.page, "open");
    await expect(
      app.page.locator('[data-stage-id="embeddings"]'),
    ).toBeVisible();

    const refreshed = app.page.waitForRequest(
      (request) => new URL(request.url()).pathname === "/api/learning",
    );
    await emitSse(app.page, "jobs.update", {
      id: "job-e2e",
      kind: "index",
      status: "running",
    });
    await refreshed;
    await expect(
      app.page.locator('[data-stage-id="embeddings"]'),
    ).toContainText(/13,696\s*\/\s*34,938/);
  });

  test("a progress-only learning.status patches the stage without a read", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/learning", body: learningView() },
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
      {
        method: "GET",
        path: "/api/labels/summary",
        body: labelsSummaryView(),
      },
    ]);

    let learningRequests = 0;
    app.page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/learning")
        learningRequests += 1;
    });

    await app.goto("/learning");
    await app.expectRoute("learning");
    await emitSse(app.page, "open");
    const stage = app.page.locator('[data-stage-id="embeddings"]');
    await expect(stage).toContainText(/13,696\s*\/\s*34,938/);
    await app.page.waitForTimeout(300);
    const before = learningRequests;

    const view = learningView();
    const status = view.status as {
      state: string;
      scope: string;
      at: string;
      stages: Array<{
        id: string;
        state: string;
        progress: Record<string, number | string>;
      }>;
    };
    await emitSse(app.page, "learning.status", {
      ...status,
      at: "2026-11-02T08:05:00Z",
      stages: status.stages.map((entry) =>
        entry.id === "embeddings"
          ? {
              ...entry,
              progress: {
                done: 20_000,
                total: 34_938,
                unit: "messages",
                embedded: 19_950,
                unchanged: 50,
                failed: 0,
              },
            }
          : entry,
      ),
    });
    await expect(stage).toContainText(/20,000\s*\/\s*34,938/);
    await expect(stage).toContainText("19,950 embedded");
    await app.page.waitForTimeout(600);
    expect(learningRequests).toBe(before);
  });

  test("a job progress tick does not force a learning read; start and completion do", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/learning", body: learningView() },
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
      {
        method: "GET",
        path: "/api/labels/summary",
        body: labelsSummaryView(),
      },
    ]);

    let learningRequests = 0;
    app.page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/learning")
        learningRequests += 1;
    });

    await app.goto("/learning");
    await app.expectRoute("learning");
    await emitSse(app.page, "open");
    await expect(
      app.page.locator('[data-stage-id="embeddings"]'),
    ).toBeVisible();
    await app.page.waitForTimeout(300);

    const started = app.page.waitForRequest(
      (request) => new URL(request.url()).pathname === "/api/learning",
    );
    await emitSse(app.page, "jobs.update", {
      id: "job-progress",
      kind: "index",
      status: "running",
      progress: { done: 1, total: 10 },
    });
    await started;
    await app.page.waitForTimeout(400);
    const afterStart = learningRequests;

    // The strip already merges every progress event; the Learning screen
    // reads only job start/completion. A tick must not force another read.
    await emitSse(app.page, "jobs.update", {
      id: "job-progress",
      kind: "index",
      status: "running",
      progress: { done: 5, total: 10 },
    });
    await app.page.waitForTimeout(600);
    expect(learningRequests).toBe(afterStart);

    const finished = app.page.waitForRequest(
      (request) => new URL(request.url()).pathname === "/api/learning",
    );
    await emitSse(app.page, "jobs.update", {
      id: "job-progress",
      kind: "index",
      status: "succeeded",
      finished_at: "2026-10-25T10:01:00Z",
    });
    await finished;
    expect(learningRequests).toBeGreaterThan(afterStart);
  });
});

// The status is the primary read; the label summary is supplementary. A
// failed summary must not blank the status or claim "none"/0, and its own row
// owns the retry. Jobs come from the shell's one model, never a per-screen
// /api/jobs read.
test("a failed label summary keeps the learning status and retries in place", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  let summaryFail = true;
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView() },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    // No hub gate snapshot: the dedicated summary read is the only source.
    { method: "GET", path: "/api/hub", body: {} },
    {
      method: "GET",
      path: "/api/labels/summary",
      handler: async ({ route }) => {
        if (summaryFail) {
          await fulfillJson(
            route,
            {
              error: { code: "internal", message: "synthetic summary failure" },
            },
            500,
          );
          return;
        }
        await fulfillJson(route, labelsSummaryView());
      },
    },
  ]);

  await app.goto("/learning");
  await app.expectRoute("learning");

  // The main status painted even though the supplementary read failed.
  await expect(app.page.locator(".learning-callout h2")).toHaveText(
    "A few answers will unlock learning",
  );
  const side = app.page.locator(".learning-side");
  const jobsRow = side.locator('.kv dt:text-is("Running jobs") + dd');
  const labelsRow = side.locator('.kv dt:text-is("Label progress") + dd');
  await expect(jobsRow).toContainText("none");
  await expect(labelsRow).toContainText("Label progress is unavailable.");

  summaryFail = false;
  await labelsRow.getByRole("button", { name: "Try again" }).click();

  await expect(side.locator(".inline-read-error")).toHaveCount(0);
  await expect(labelsRow).toHaveCount(0);
});

// The Learning screen counts the questions stage, not the gate's open flag:
// one certified question is progress, and "Everything is ready" is reserved
// for a full count. The direct route waits for the delayed Hub snapshot.
test("an open gate names partial certification on a direct load and reserves ready for a full count", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  let hub = hubCertificationView(1, 17);
  let hubHold: Promise<void> = Promise.resolve();
  let releaseHub: (() => void) | null = null;
  let hubAsked = false;
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/learning",
      body: learningView({ open: true, preview: false }),
    },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    {
      method: "GET",
      path: "/api/labels/summary",
      body: labelsSummaryView(),
    },
    {
      method: "GET",
      path: "/api/hub",
      handler: async ({ route }) => {
        hubAsked = true;
        await hubHold;
        await fulfillJson(route, hub);
      },
    },
  ]);

  const openLearning = async () => {
    hubHold = new Promise<void>((resolve) => {
      releaseHub = resolve;
    });
    hubAsked = false;
    await app.page.goto("about:blank");
    await app.goto("/learning");
    // Bounded by the assertion timeout, so a boot that never asks for the
    // snapshot names that fact instead of stalling to the 45 s test timeout
    // and reading as an unrelated spec.
    await expect
      .poll(() => hubAsked, { message: "the app never read /api/hub" })
      .toBe(true);
    releaseHub?.();
    await hubHold;
    await app.expectRoute("learning");
  };

  await openLearning();
  const callout = app.page.locator(".learning-callout");
  await expect(callout.locator("h2")).toHaveText("Certification in progress");
  await expect(callout.locator("p.lede")).toHaveText(
    "1 of 17 questions are certified at the target. The others abstain until they certify.",
  );
  await expect(callout).toContainText("1 of 17 certified");
  await expect(
    callout.locator(".pill", { hasText: "Partly certified" }),
  ).toHaveClass("pill warn");
  await expect(callout.getByText("Ready", { exact: true })).toHaveCount(0);
  await expect(callout).not.toContainText("Everything is ready");

  hub = hubCertificationView(17, 17);
  await openLearning();
  await expect(callout.locator("h2")).toHaveText("Everything is ready");
  await expect(callout).toContainText("17 of 17 certified");
  await expect(callout.locator(".pill", { hasText: "Ready" })).toHaveClass(
    "pill done",
  );
  await expect(
    callout.locator(".pill", { hasText: "Partly certified" }),
  ).toHaveCount(0);
  await expect(callout).not.toContainText("Certification in progress");
});

// A status without steps is not a timeline: the section says so instead of
// painting an empty scroller.
test("a learning status without steps shows an empty timeline", async ({
  app,
}) => {
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/learning",
      body: learningView({
        open: true,
        status: {
          state: "learning",
          scope: "store:e2e",
          at: "2026-11-02T08:00:00Z",
          stages: [],
        },
      }),
    },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    { method: "GET", path: "/api/labels/summary", body: labelsSummaryView() },
  ]);

  await app.goto("/learning");
  await app.expectRoute("learning");
  const timeline = app.page.locator(".learning-body .empty");
  await expect(timeline).toContainText("No steps are reported yet");
  await expect(timeline).toContainText("Learning has not reported its steps.");
  await expect(app.page.locator(".timeline")).toHaveCount(0);
});
