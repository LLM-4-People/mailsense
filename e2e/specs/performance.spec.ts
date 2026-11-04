import {
  agendaView,
  messageView,
  priorityView,
  questionsView,
  syntheticMailRow,
  triageResult,
  type MessagesPage,
} from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { installFakeEventSource } from "../fixtures/sse";
import { deferred, expect, test } from "../fixtures/test";

const ALL_MAIL_PAGE_SIZE = 100;
const ALL_MAIL_PAGES = 20;
const ALL_MAIL_TOTAL = ALL_MAIL_PAGE_SIZE * ALL_MAIL_PAGES;

// The first twenty 100-row pages of one keyset population. The cursor is the
// page number, so a Load more that must not run is observable as a missing
// request rather than only as a missing row.
function cappedMailPage(before = ""): MessagesPage {
  const page = before ? Number(before.replace("cursor-", "")) : 0;
  const start = page * ALL_MAIL_PAGE_SIZE;
  const next = page + 1 < ALL_MAIL_PAGES ? "cursor-" + String(page + 1) : "";
  return {
    messages: Array.from({ length: ALL_MAIL_PAGE_SIZE }, (_, index) =>
      syntheticMailRow(5000 + start + index),
    ),
    total: ALL_MAIL_TOTAL,
    counts: {
      inbox: ALL_MAIL_TOTAL,
      sent: 0,
      drafts: 0,
      deleted: 0,
      junk: 0,
      archive: 0,
      other: 0,
    },
    next_before: next,
    has_more: !!next,
  };
}

// The list fills the sentinel one page at a time while it stays in the
// viewport, so a click may complete more than one page. Drive it until the
// cap is reached instead of assuming one page per click.
async function fillToCap(page: Parameters<typeof mockApi>[0]): Promise<void> {
  const rows = page.locator(".all-mail-row");
  const loadMore = page.locator(".pager").getByRole("button", { name: "Load more" });
  for (let attempt = 0; attempt < 8; attempt += 1) {
    if ((await rows.count()) >= 300) return;
    if ((await loadMore.count()) === 0) return;
    await loadMore.click({ timeout: 2_000 }).catch(() => undefined);
    await page.waitForTimeout(60);
  }
}

test.describe("boot and list performance", () => {
  test("boot paints Home after status, language and one gate read", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    let gateCalls = 0;
    await mockApi(app.page, [
      { method: "GET", path: "/api/hub", body: {} },
      { method: "GET", path: "/api/priority", body: priorityView() },
      { method: "GET", path: "/api/agenda", body: agendaView() },
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "GET",
        path: "/api/gate",
        handler: async ({ route }) => {
          gateCalls += 1;
          await fulfillJson(route, {
            allowed: true,
            open: true,
            preview: false,
          });
        },
      },
    ]);

    // Learning and the job strip are chrome. Hold both so the first content
    // paint can be observed while they are still unanswered.
    const learning = deferred();
    const jobs = deferred();
    let learningSettled = false;
    let jobsSettled = false;
    await app.page.route("**/api/learning*", async (route) => {
      await learning.promise;
      learningSettled = true;
      await route.fallback();
    });
    await app.page.route("**/api/jobs*", async (route) => {
      await jobs.promise;
      jobsSettled = true;
      await route.fallback();
    });

    const pathsAtPaint: string[] = [];
    app.page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname.startsWith("/api/")) pathsAtPaint.push(url.pathname);
    });

    await app.goto("/hub");
    await app.expectRoute("hub");
    await expect(app.page.locator(".home-head-heading")).toBeVisible();

    // The first paint did not wait on the two chrome reads.
    expect(learningSettled).toBe(false);
    expect(jobsSettled).toBe(false);
    expect(gateCalls).toBe(1);
    await expect(
      app.page.locator('#main .screen[aria-busy="true"]'),
    ).toHaveCount(0);

    // Boot no longer ships hidden screens: only Home's own reads start here.
    await app.page.waitForTimeout(300);
    const hidden = pathsAtPaint.filter(
      (path) =>
        path === "/api/triage/summary" ||
        path.startsWith("/api/triage?") ||
        path.startsWith("/api/conversations") ||
        path.startsWith("/api/companies") ||
        path === "/api/brief",
    );
    expect(hidden).toEqual([]);

    learning.resolve();
    jobs.resolve();
    await expect.poll(() => learningSettled).toBe(true);
    await expect.poll(() => jobsSettled).toBe(true);
    expect(gateCalls).toBe(1);
  });

  test("All Mail stops at the mounted-row cap with a first-N notice", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    let gateCalls = 0;
    const messageRequests: string[] = [];
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/gate",
        handler: async ({ route }) => {
          gateCalls += 1;
          await fulfillJson(route, {
            allowed: true,
            open: true,
            preview: false,
          });
        },
      },
      {
        method: "GET",
        path: "/api/messages",
        handler: async ({ route, url }) => {
          const before = url.searchParams.get("before") || "";
          messageRequests.push(before);
          await fulfillJson(route, cappedMailPage(before));
        },
      },
      {
        method: "GET",
        path: /^\/api\/messages\/\d+$/,
        handler: async ({ route, url }) => {
          await fulfillJson(route, messageView(Number(url.pathname.split("/").pop())));
        },
      },
      {
        method: "GET",
        path: /^\/api\/triage\/\d+$/,
        handler: async ({ route, url }) => {
          await fulfillJson(route, triageResult(Number(url.pathname.split("/").pop())));
        },
      },
      { method: "GET", path: "/api/questions", body: questionsView() },
    ]);

    await app.goto("/mail");
    await app.expectRoute("mail");
    const rows = app.page.locator(".all-mail-row");
    await expect(rows).toHaveCount(ALL_MAIL_PAGE_SIZE);

    // The list fills one page per sentinel pass while the sentinel stays in
    // the viewport, so drive it to the cap; the sentinel then leaves and the
    // fourth page is never requested.
    await fillToCap(app.page);
    await expect(rows).toHaveCount(300);

    const pager = app.page.locator(".pager");
    await expect(pager).toContainText(
      "Showing the first 300 of 2,000 messages.",
    );
    await expect(
      pager.getByRole("button", { name: "Load more" }),
    ).toHaveCount(0);
    // The cap is a request bound as well as a row bound: never a fourth page.
    expect(messageRequests.length).toBe(3);
    expect(gateCalls).toBe(1);

    // The cap never removes a mounted row, so selection still reaches the
    // oldest visible message.
    await rows.nth(299).click();
    await expect(rows.nth(299)).toHaveAttribute("aria-current", "true");
    await expect(rows.nth(0)).not.toHaveAttribute("aria-current", "true");
  });
});
