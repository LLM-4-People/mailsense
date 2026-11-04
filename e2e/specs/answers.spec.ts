import { learningView, messageView, questionsView } from "../fixtures/data";
import { fulfillJson, mockApi, type MockContext } from "../fixtures/mock";
import { installFakeEventSource } from "../fixtures/sse";
import { expect, hoverNav, test } from "../fixtures/test";

const labelQuestion = {
  name: "category",
  type: "choice",
  instructions: "Pick the category that best describes the message.",
  options: [
    { name: "work", description: "A work update." },
    { name: "action", description: "An action is requested." },
  ],
};

const answerRows = Array.from({ length: 6 }, (_, index) => {
  const id = 6 - index;
  return {
    id,
    message_id: 1001 + index,
    question: "category",
    source: id % 2 === 0 ? "judge" : "manual",
    value: id % 2 === 1 ? "work" : "action",
    at: `2026-10-${String(10 + index).padStart(2, "0")}T09:00:00Z`,
  };
});

// The one current-label page: two rows per cursor, the cursor is the last
// row id, and has_more follows the fixture length.
async function answerPage({ route, url }: MockContext) {
  const message = Number(url.searchParams.get("message") || 0);
  if (message) {
    await fulfillJson(route, {
      items: answerRows.filter((row) => row.message_id === message),
      next_cursor: 0,
      has_more: false,
    });
    return;
  }
  const cursor = Number(url.searchParams.get("cursor") || 0);
  const at = cursor === 0
    ? 0
    : answerRows.findIndex((row) => row.id === cursor) + 1;
  const items = answerRows.slice(at, at + 2);
  const last = items[items.length - 1];
  await fulfillJson(route, {
    items,
    next_cursor: last ? last.id : 0,
    has_more: at + items.length < answerRows.length,
  });
}

test("hovering a nav destination keeps the loaded Answers page", async ({ app }) => {
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/questions",
      body: { ...questionsView(), questions: [labelQuestion] },
    },
    {
      method: "GET",
      path: "/api/labels/summary",
      body: {
        manual: 3,
        judge: 3,
        review: 0,
        questions: [{ question: "category", manual: 3, judge: 3, needed: 6, review: 0 }],
      },
    },
    { method: "GET", path: "/api/answers", handler: answerPage },
  ]);

  await app.goto("/answers?question=category");
  await app.expectRoute("answers");
  const rows = app.page.locator(".answers-table tbody tr");
  await expect(rows).toHaveCount(2);
  await expect(app.page.locator(".pager")).toContainText("2 shown");

  await app.page.getByRole("button", { name: "Load more" }).click();
  await expect(rows).toHaveCount(4);
  await expect(app.page.locator(".pager")).toContainText("4 shown");

  // Answers has no sidebar entry, so point an existing nav listener at its
  // href. The bare route is a different key from the filtered screen; this is
  // the prefetch that used to replace state.answersPage with page one.
  const proxy = app.page.locator('[data-nav="sources"]');
  await proxy.evaluate((node) => node.setAttribute("href", "#/answers"));
  await hoverNav(app.page, "sources");

  await expect(rows).toHaveCount(4);
  await expect(app.page.locator(".pager")).toContainText("4 shown");

  // Load more compares the page token with the mounted screen. A cached
  // remount reuses the same node without rerunning its render, so the
  // control must keep paging and return focus to the keyboard.
  await app.page.getByRole("button", { name: "Load more" }).click();
  await expect(rows).toHaveCount(6);
  await expect(app.page.locator(".pager")).toContainText("6 shown");
  await expect(app.page.locator(".pager")).toContainText("End of the answers.");
});

test("Answers keeps paging after a cached remount and restores focus", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/questions",
      body: { ...questionsView(), questions: [labelQuestion] },
    },
    {
      method: "GET",
      path: "/api/labels/summary",
      body: {
        manual: 3,
        judge: 3,
        review: 0,
        questions: [{ question: "category", manual: 3, judge: 3, needed: 6, review: 0 }],
      },
    },
    { method: "GET", path: "/api/answers", handler: answerPage },
  ]);

  await app.goto("/answers?question=category");
  await app.expectRoute("answers");
  const rows = app.page.locator(".answers-table tbody tr");
  await expect(rows).toHaveCount(2);

  const loadMore = app.page.getByRole("button", { name: "Load more" });
  await loadMore.focus();
  await app.page.keyboard.press("Enter");
  await expect(rows).toHaveCount(4);
  await expect(loadMore).toBeFocused();
  await app.page.locator(".answers-screen").evaluate((node) => node.setAttribute("data-e2e-cached", "answers"));

  // Back re-appends the cached Answers node without rerunning renderAnswers.
  // The captured render sequence moved on; the page model did not.
  await app.page.locator('[data-nav="hub"]').click();
  await app.expectRoute("hub");
  await app.page.goBack();
  await app.expectRoute("answers");
  await expect(app.page.locator('.answers-screen[data-e2e-cached="answers"]')).toHaveCount(1);
  await expect(rows).toHaveCount(4);

  const restored = app.page.getByRole("button", { name: "Load more" });
  await restored.focus();
  await app.page.keyboard.press("Enter");
  await expect(rows).toHaveCount(6);
  await expect(app.page.locator(".pager")).toContainText("6 shown");
  await expect(app.page.locator(".pager")).toContainText("End of the answers.");
  await expect(restored).toBeHidden();
});

test("opening and closing an Answers detail keeps the loaded pages, scroll and focus", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/questions",
      body: { ...questionsView(), questions: [labelQuestion] },
    },
    {
      method: "GET",
      path: "/api/labels/summary",
      body: {
        manual: 3,
        judge: 3,
        review: 0,
        questions: [{ question: "category", manual: 3, judge: 3, needed: 6, review: 0 }],
      },
    },
    { method: "GET", path: "/api/answers", handler: answerPage },
    { method: "GET", path: "/api/messages/1004", body: messageView(1004) },
  ]);

  await app.page.setViewportSize({ width: 1280, height: 600 });
  await app.goto("/answers?question=category");
  await app.expectRoute("answers");
  const rows = app.page.locator(".answers-table tbody tr");
  const body = app.page.locator(".answers-body");
  await expect(rows).toHaveCount(2);
  await app.page.getByRole("button", { name: "Load more" }).click();
  await expect(rows).toHaveCount(4);

  // The route is the mounted screen: opening a row must patch this node,
  // never replace it with a fresh page-one render.
  await app.page.locator(".answers-screen").evaluate((node) => {
    node.setAttribute("data-e2e-stable", "answers");
  });
  // Give the bounded route body room to scroll, then put the list at a real
  // offset the detail must not reset.
  await body.evaluate((node) => {
    const filler = document.createElement("div");
    filler.style.height = "1200px";
    node.append(filler);
  });
  await body.evaluate((node) => { node.scrollTop = 120; });

  const open = rows.nth(3).getByRole("link", { name: /Open/ });
  await open.focus();
  const scrollBefore = await body.evaluate((node) => node.scrollTop);
  await app.page.keyboard.press("Enter");

  await expect(app.page).toHaveURL(/message=1004/);
  await expect(app.page.locator(".answers-detail")).toContainText("Synthetic message 1004");
  await expect(rows).toHaveCount(4);
  await expect(app.page.locator('.answers-screen[data-e2e-stable="answers"]')).toHaveCount(1);
  expect(await body.evaluate((node) => node.scrollTop)).toBe(scrollBefore);
  await expect(open).toBeFocused();
  await expect(rows.nth(3)).toHaveClass(/selected/);

  await app.page.goBack();
  await expect(app.page).not.toHaveURL(/message=/);
  await expect(app.page.locator(".answers-screen")).toBeVisible();
  await expect(rows).toHaveCount(4);
  await expect(app.page.locator(".answers-detail")).toHaveCount(0);
  await expect(app.page.locator('.answers-screen[data-e2e-stable="answers"]')).toHaveCount(1);
  expect(await body.evaluate((node) => node.scrollTop)).toBe(scrollBefore);
  await expect(open).toBeFocused();
});

// The Answers source switch is a route switch with a stable focus key: a
// keyboard activation keeps focus on the chosen segment after the rebuild.
test("a keyboard Answers source switch returns focus to the active tab", async ({ app }) => {
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/questions",
      body: { ...questionsView(), questions: [labelQuestion] },
    },
    {
      method: "GET",
      path: "/api/labels/summary",
      body: {
        manual: 3,
        judge: 3,
        review: 0,
        questions: [{ question: "category", manual: 3, judge: 3, needed: 6, review: 0 }],
      },
    },
    { method: "GET", path: "/api/answers", handler: answerPage },
  ]);

  await app.goto("/answers?question=category");
  await app.expectRoute("answers");
  const sources = app.page.locator(".answers-filters .segmented");
  const judge = sources.getByRole("button", { name: "Judge", exact: true });
  await judge.focus();
  await app.page.keyboard.press("Enter");
  await expect(app.page).toHaveURL(/source=judge/);
  await expect(judge).toHaveAttribute("aria-current", "page");
  await expect(judge).toBeFocused();
});

// A failed list read belongs inside the list section: the header, filters and
// summary stay mounted and the retry re-reads the rows.
test("a failed answers list keeps the header and filters", async ({ app }) => {
  let fail = true;
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/questions",
      body: { ...questionsView(), questions: [labelQuestion] },
    },
    {
      method: "GET",
      path: "/api/labels/summary",
      body: {
        manual: 3,
        judge: 3,
        review: 0,
        questions: [{ question: "category", manual: 3, judge: 3, needed: 6, review: 0 }],
      },
    },
    {
      method: "GET",
      path: "/api/answers",
      handler: async (context) => {
        if (fail) {
          await fulfillJson(
            context.route,
            { code: "internal", message: "Synthetic answers failure" },
            500,
          );
          return;
        }
        await answerPage(context);
      },
    },
  ]);

  await app.goto("/answers?question=category");
  await app.expectRoute("answers");
  await expect(app.page.locator(".answers-head")).toContainText("Answer history");
  await expect(app.page.locator(".answers-filters")).toBeVisible();
  const list = app.page.locator(".answers-body .empty");
  await expect(list).toContainText("Answers unavailable");
  await expect(list).toContainText("The answer list could not be loaded.");
  await expect(app.page.locator(".answers-table tbody tr")).toHaveCount(0);

  fail = false;
  await list.getByRole("button", { name: "Try again" }).click();
  await expect(app.page.locator(".answers-table tbody tr")).toHaveCount(2);
  await expect(app.page.locator(".answers-filters")).toBeVisible();
});
