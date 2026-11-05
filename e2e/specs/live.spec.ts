import { fulfillJson, mockApi } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

test.describe("global job strip", () => {
  test("a job appears, patches progress in place and leaves after finishing", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    // The real preview server may start a housekeeping job; the strip must be
    // empty until this test emits its own job.
    await mockApi(app.page, [
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    ]);
    await app.goto("/hub");
    await app.expectRoute("hub");
    await emitSse(app.page, "open");

    const strip = app.page.locator("#job-strip");
    await expect(strip).toBeHidden();

    await emitSse(app.page, "jobs.update", {
      id: "job-live",
      kind: "index",
      name: "Semantic index",
      status: "running",
      progress: {
        done: 1_234,
        total: 34_938,
        unit: "messages",
        message: "Embedding mail",
      },
      started_at: "2026-10-27T22:00:00Z",
    });

    const summary = strip.locator(".job-summary");
    await expect(strip).toBeVisible();
    await expect(strip.locator("progress")).toHaveCount(1);
    await expect(summary).toContainText("1 task running");
    await expect(summary).toContainText("Semantic index");
    await expect(summary).toContainText("1,234 / 34,938 messages");
    await expect(summary).toContainText("4%");
    await expect
      .poll(() => strip.locator("progress").evaluate((node) => node.value))
      .toBe(1_234);
    // The producer sentence is an English diagnostic; the aggregate strip
    // shows localized structure and never leaks it into the main surface.
    await expect(summary).not.toContainText("Embedding mail");

    await emitSse(app.page, "jobs.update", {
      id: "job-live",
      kind: "index",
      name: "Semantic index",
      status: "running",
      progress: {
        done: 20_000,
        total: 34_938,
        unit: "messages",
        message: "Embedding mail",
      },
      started_at: "2026-10-27T22:00:00Z",
    });
    await expect(summary).toContainText("20,000 / 34,938");
    await expect(strip.locator("progress")).toHaveCount(1);
    await expect
      .poll(() => strip.locator("progress").evaluate((node) => node.value))
      .toBe(20_000);

    await emitSse(app.page, "jobs.update", {
      id: "job-live",
      kind: "index",
      name: "Semantic index",
      status: "succeeded",
      progress: {
        done: 34_938,
        total: 34_938,
        unit: "messages",
        message: "",
      },
      started_at: "2026-10-27T22:00:00Z",
      finished_at: new Date().toISOString(),
    });
    await expect(summary).toContainText("Semantic index (succeeded)");
    await expect(strip.locator("progress")).toBeHidden();
    await expect(strip).toBeHidden({ timeout: 7_000 });
  });

  test("a staged job folds into the aggregate summary with its ETA", async ({ app }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    ]);
    await app.goto("/hub");
    await app.expectRoute("hub");
    await emitSse(app.page, "open");

    await emitSse(app.page, "jobs.update", {
      id: "job-stage",
      kind: "layer1",
      name: "Local classifier",
      status: "running",
      progress: {
        done: 3,
        total: 17,
        unit: "questions",
        stage: "fold",
        detail: "topic",
        step_done: 2,
        step_total: 5,
        eta: 90_000_000_000,
      },
      started_at: "2026-11-02T08:00:00Z",
    });

    const summary = app.page.locator("#job-strip .job-summary");
    // The producer's unit is part of the aggregate count, so it never says
    // "3 / 17" without saying what is counted.
    await expect(summary).toContainText("3 / 17 questions");
    await expect(summary).toContainText("18%");
    await expect(summary).toContainText("about 1.5 min left");

    // The answer phase counts messages, and the aggregate follows it.
    await emitSse(app.page, "jobs.update", {
      id: "job-stage",
      kind: "layer1",
      name: "Local classifier",
      status: "running",
      progress: {
        done: 1500,
        total: 104289,
        unit: "messages",
        stage: "answer",
        step_done: 1,
        step_total: 3,
        eta: 45_000_000_000,
      },
      started_at: "2026-11-02T08:00:00Z",
    });
    await expect(summary).toContainText("1,500 / 104,289 messages");
    await expect(summary).toContainText("1%");
  });

  test("two concurrent jobs share one aggregate meter and keep per-job cancel", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    const sync = {
      id: "job-sync",
      kind: "sync",
      name: "sync",
      status: "running",
      progress: { done: 10, total: 100, unit: "messages" },
      started_at: "2026-11-02T08:00:00Z",
    };
    const index = {
      id: "job-index",
      kind: "index",
      name: "Semantic index",
      status: "running",
      progress: { done: 30, total: 300, unit: "messages" },
      started_at: "2026-11-02T08:01:00Z",
    };
    await mockApi(app.page, [
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
      {
        method: "POST",
        path: "/api/jobs/job-sync/cancel",
        handler: async ({ route }) => {
          await fulfillJson(route, {
            ...sync,
            status: "cancelled",
            finished_at: new Date().toISOString(),
          });
        },
      },
    ]);
    await app.goto("/hub");
    await app.expectRoute("hub");
    await emitSse(app.page, "open");

    await emitSse(app.page, "jobs.update", sync);
    await emitSse(app.page, "jobs.update", index);

    const strip = app.page.locator("#job-strip");
    const summary = strip.locator(".job-summary");
    await expect(strip).toBeVisible();
    await expect(strip.locator("progress")).toHaveCount(1);
    await expect(summary).toContainText("2 tasks running");
    await expect(summary).toContainText("Mail sync");
    await expect(summary).toContainText("Semantic index");
    // Aggregate: (10 + 30) / (100 + 300) = 10%.
    await expect(summary).toContainText("40 / 400 messages");
    await expect(summary).toContainText("10%");
    await expect
      .poll(() => strip.locator("progress").evaluate((node) => node.value))
      .toBe(40);
    await expect
      .poll(() => strip.locator("progress").evaluate((node) => node.max))
      .toBe(400);

    await expect(
      strip.getByRole("button", { name: /^Cancel / }),
    ).toHaveCount(2);
    // The visible label names the task, not a bare "Cancel": two jobs can run
    // at once, and the accessible name alone did not help a sighted user.
    await expect(
      strip.getByRole("button", { name: "Cancel Mail sync" }),
    ).toHaveText("Cancel Mail sync");
    await expect(
      strip.getByRole("button", { name: "Cancel Semantic index" }),
    ).toHaveText("Cancel Semantic index");
    await strip.getByRole("button", { name: "Cancel Mail sync" }).click();

    await expect(summary).toContainText("Mail sync (cancelled)");
    await expect(summary).toContainText("1 task running");
    await expect(summary).toContainText("30 / 300 messages");
    await expect(strip.getByRole("button", { name: "Cancel Mail sync" })).toHaveCount(0);
    await expect(
      strip.getByRole("button", { name: "Cancel Semantic index" }),
    ).toBeVisible();
    await expect(strip.locator("progress")).toHaveCount(1);
  });

  test("a paused job stays in the running aggregate and names its ceiling", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    ]);
    await app.goto("/hub");
    await app.expectRoute("hub");
    await emitSse(app.page, "open");

    const sync = {
      id: "job-sync",
      kind: "sync",
      name: "sync",
      status: "running",
      progress: { done: 10, total: 100, unit: "messages" },
      started_at: "2026-11-02T08:00:00Z",
      paused: { resource: "cpu", percent: 92, limit: 80 },
    };
    const index = {
      id: "job-index",
      kind: "index",
      name: "Semantic index",
      status: "running",
      progress: { done: 30, total: 300, unit: "messages" },
      started_at: "2026-11-02T08:01:00Z",
      paused: { resource: "ram", percent: 91, limit: 85 },
    };
    await emitSse(app.page, "jobs.update", sync);
    await emitSse(app.page, "jobs.update", index);

    const strip = app.page.locator("#job-strip");
    const summary = strip.locator(".job-summary");
    await expect(strip).toBeVisible();
    // A paused job is still running: the count, the aggregate and its own
    // ceiling note all stay, and the strip still owns one progress bar.
    await expect(summary).toContainText("2 tasks running");
    await expect(summary).toContainText("40 / 400 messages");
    await expect(summary).toContainText("10%");
    await expect(summary).toContainText("Mail sync paused: CPU at 92 % (limit 80 %)");
    await expect(summary).toContainText("Semantic index paused: Memory at 91 % (limit 85 %)");
    await expect(strip.locator("progress")).toHaveCount(1);
    await expect(
      strip.getByRole("button", { name: "Cancel Mail sync" }),
    ).toBeVisible();
    await expect(
      strip.getByRole("button", { name: "Cancel Semantic index" }),
    ).toBeVisible();

    // Clearing the pause leaves the same running job under its plain name.
    await emitSse(app.page, "jobs.update", { ...sync, paused: undefined });
    await expect(summary).not.toContainText("Mail sync paused");
    await expect(summary).toContainText("Mail sync");
    await expect(summary).toContainText("2 tasks running");
    await expect(strip.locator("progress")).toHaveCount(1);
    await expect(strip.getByRole("button", { name: "Cancel Mail sync" })).toBeVisible();
  });

  test("a starting job is named with an indeterminate aggregate", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    ]);
    await app.goto("/hub");
    await app.expectRoute("hub");
    await emitSse(app.page, "open");

    await emitSse(app.page, "jobs.update", {
      id: "job-starting",
      kind: "sync",
      name: "sync",
      status: "running",
      started_at: "2026-11-02T08:00:00Z",
    });

    const strip = app.page.locator("#job-strip");
    const summary = strip.locator(".job-summary");
    await expect(summary).toContainText("1 task running");
    await expect(summary).toContainText("Mail sync");
    await expect(strip.locator("progress")).toHaveCount(1);
    expect(
      await strip.locator("progress").evaluate((node) => node.hasAttribute("value")),
    ).toBe(false);

    // A second job that reports work makes the same meter determinate; the
    // starting job stays named in the summary.
    await emitSse(app.page, "jobs.update", {
      id: "job-index",
      kind: "index",
      name: "Semantic index",
      status: "running",
      progress: { done: 30, total: 300, unit: "messages" },
      started_at: "2026-11-02T08:01:00Z",
    });
    await expect(summary).toContainText("2 tasks running");
    await expect(summary).toContainText("Mail sync");
    await expect(summary).toContainText("Semantic index");
    await expect(summary).toContainText("30 / 300");
    await expect
      .poll(() => strip.locator("progress").evaluate((node) => node.value))
      .toBe(30);
  });

  test("a failed job stays named and Dismiss removes the one strip", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    ]);
    await app.goto("/hub");
    await app.expectRoute("hub");
    await emitSse(app.page, "open");

    await emitSse(app.page, "jobs.update", {
      id: "job-failed",
      kind: "index",
      name: "Semantic index",
      status: "failed",
      error: "synthetic failure",
      progress: { done: 5, total: 10, unit: "messages" },
      started_at: "2026-11-02T08:00:00Z",
      finished_at: new Date().toISOString(),
    });

    const strip = app.page.locator("#job-strip");
    const summary = strip.locator(".job-summary");
    await expect(summary).toContainText("Background tasks");
    // A failure names its cause: the owner must not see "failed" with no
    // reason, and the strip keeps it until Dismiss.
    await expect(summary).toContainText("Semantic index failed: synthetic failure");
    await expect(strip.locator("progress")).toBeHidden();
    const dismiss = strip.getByRole("button", { name: "Dismiss Semantic index" });
    await expect(dismiss).toBeVisible();
    await dismiss.click();
    await expect(strip).toBeHidden();
    await expect(strip.locator(".job-summary")).toHaveCount(0);
  });

  test("a reconnect re-reads the live snapshot", async ({ app }) => {
    await installFakeEventSource(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    ]);

    let jobReads = 0;
    app.page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/jobs") jobReads += 1;
    });

    await app.goto("/hub");
    await app.expectRoute("hub");
    await emitSse(app.page, "open");
    const before = jobReads;

    // The browser reports a dropped stream, then a new open. The shell must
    // re-base its live reads instead of trusting the gap.
    await emitSse(app.page, "error");
    await emitSse(app.page, "open");
    await expect.poll(() => jobReads, { timeout: 5_000 }).toBeGreaterThan(before);

    // A job that finished while the stream was down has no terminal event.
    // The reconnect snapshot is authoritative: its absence removes the chip
    // instead of leaving a permanent "running" job.
    await emitSse(app.page, "jobs.update", {
      id: "job-orphan",
      kind: "index",
      name: "Semantic index",
      status: "running",
      started_at: "2026-11-02T08:00:00Z",
    });
    const orphan = app.page.getByRole("button", { name: "Cancel Semantic index" });
    await expect(orphan).toBeVisible();
    await emitSse(app.page, "error");
    await emitSse(app.page, "open");
    await expect(orphan).toHaveCount(0);
  });
});
