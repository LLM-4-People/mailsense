import {
  AT,
  emptyMessageListPage,
  messageListPage,
  messageView,
  msg,
  priorityItem,
  priorityView,
  progressiveMailPage,
  progressiveSenderMailPage,
  progressiveUnreadMailPage,
  questionsView,
  searchView,
  triageResult,
  type MessagesPage,
} from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { emitSse, installFakeEventSource, installFakeStreamWithClock } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

type MailMockOptions = {
  messagesPage?: (
    role: string,
    before: string,
    sender: string,
    unread: boolean,
  ) => MessagesPage;
  priorityPage?: (filter: string, limit: number, offset: number) => unknown;
  priorityFail?: () => boolean;
};

async function mailMocks(
  page: Parameters<typeof mockApi>[0],
  options: MailMockOptions = {},
) {
  await mockApi(page, [
    {
      method: "GET",
      path: "/api/priority",
      handler: async ({ route, url }) => {
        const filter = url.searchParams.get("filter") || "all";
        const limit = url.searchParams.get("limit") || "50";
        const offset = Number(url.searchParams.get("offset") || "0");
        if (limit === "1") {
          const totals: Record<string, number> = {
            all: 2,
            reply: 1,
            deadline: 1,
          };
          await fulfillJson(route, {
            items: [],
            total: totals[filter] ?? 0,
            generated_at: "2026-10-25T10:00:00Z",
            preview: true,
          });
          return;
        }
        if (options.priorityPage) {
          if (options.priorityFail && options.priorityFail()) {
            await fulfillJson(route, { code: "internal", message: "Synthetic priority failure" }, 500);
            return;
          }
          await fulfillJson(route, options.priorityPage(filter, Number(limit), offset));
          return;
        }
        await fulfillJson(route, priorityView(filter));
      },
    },
    {
      method: "GET",
      path: "/api/messages",
      handler: async ({ route, url }) => {
        const role = url.searchParams.get("role") || "all";
        const before = url.searchParams.get("before") || "";
        const sender = url.searchParams.get("sender") || "";
        const unreadParam = (url.searchParams.get("unread") || "").toLowerCase();
        const unread = unreadParam === "true" || unreadParam === "1";
        await fulfillJson(
          route,
          options.messagesPage
            ? options.messagesPage(role, before, sender, unread)
            : messageListPage(role, before, sender, unread),
        );
      },
    },
    {
      method: "GET",
      path: /^\/api\/messages\/\d+$/,
      handler: async ({ route, url }) => {
        const id = Number(url.pathname.split("/").pop());
        await fulfillJson(route, messageView(id));
      },
    },
    {
      method: "GET",
      path: /^\/api\/triage\/\d+$/,
      handler: async ({ route, url }) => {
        const id = Number(url.pathname.split("/").pop());
        await fulfillJson(route, triageResult(id));
      },
    },
    { method: "GET", path: "/api/questions", body: questionsView() },
    { method: "GET", path: "/api/search", body: searchView() },
  ]);
}

type FirstMailFrame = {
  count: number;
  visible: boolean;
  id: string;
  textLength: number;
};

// Records the first connected all-mail batch before the deferred rows can be
// appended. Home also renders compact .mail-row nodes, so the observer and
// every assertion are scoped to .all-mail-row.
async function observeFirstMailFrame(page: Parameters<typeof mockApi>[0]) {
  await page.evaluate(() => {
    const holder = window as unknown as { __mailFirstFrame?: FirstMailFrame };
    holder.__mailFirstFrame = undefined;
    const record = () => {
      const rows = document.querySelectorAll<HTMLElement>(".all-mail-row");
      if (!rows.length || holder.__mailFirstFrame) return;
      const first = rows[0];
      const box = first.getBoundingClientRect();
      holder.__mailFirstFrame = {
        count: rows.length,
        visible: box.width > 0 && box.height > 0,
        id: first.dataset.messageId || "",
        textLength: (first.textContent || "").length,
      };
    };
    const observer = new MutationObserver(record);
    observer.observe(document.documentElement, { childList: true, subtree: true });
  });
}

async function firstMailFrame(
  page: Parameters<typeof mockApi>[0],
): Promise<FirstMailFrame | null> {
  return page.evaluate(
    () =>
      (window as unknown as { __mailFirstFrame?: FirstMailFrame })
        .__mailFirstFrame || null,
  );
}

// observeMailListMounts counts the screens that carry a .mail-list. A
// refresh that rebuilds the All-Mail pane mounts one more; an in-place
// append or a sentinel repaint does not.
async function observeMailListMounts(
  page: Parameters<typeof mockApi>[0],
): Promise<void> {
  await page.evaluate(() => {
    const holder = window as unknown as {
      __allMailListMounts?: number;
      __allMailListObserver?: MutationObserver;
    };
    holder.__allMailListObserver?.disconnect();
    const main = document.getElementById("main");
    holder.__allMailListMounts = 0;
    holder.__allMailListObserver = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of Array.from(record.addedNodes)) {
          if (node instanceof HTMLElement && node.querySelector(".mail-list")) {
            holder.__allMailListMounts = (holder.__allMailListMounts || 0) + 1;
          }
        }
      }
    });
    if (main) holder.__allMailListObserver.observe(main, { childList: true });
  });
}

async function mailListMounts(
  page: Parameters<typeof mockApi>[0],
): Promise<number> {
  return page.evaluate(
    () =>
      (window as unknown as { __allMailListMounts?: number })
        .__allMailListMounts || 0,
  );
}

// The 100-row page leaves the sentinel below the viewport. Playwright's
// scroll-into-view can make the IntersectionObserver start the page and
// replace the button mid-click, so focus without scrolling and dispatch the
// real click handler. The observer-driven path stays covered by the
// scroll-to-bottom checks.
async function clickLoadMore(
  page: Parameters<typeof mockApi>[0],
): Promise<void> {
  await page
    .locator(".pager")
    .getByRole("button", { name: "Load more" })
    .evaluate((node) => {
      const button = node as HTMLElement;
      button.focus({ preventScroll: true });
      button.click();
    });
}

test.describe("Mail", () => {
  test("Important lists mail and the viewer renders the selected message", async ({
    app,
  }) => {
    await mailMocks(app.page);
    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");

    await expect(
      app.page.locator('.mail-row[data-message-id="101"]'),
    ).toBeVisible();
    await expect(
      app.page.locator('.mail-row[data-message-id="102"]'),
    ).toBeVisible();
    // The subject hierarchy and the concrete action are visible on the
    // ranked row, not only after opening the message.
    await expect(
      app.page.locator('.mail-row[data-message-id="101"]'),
    ).toContainText("Billing or account › Payment");
    await expect(
      app.page.locator('.mail-row[data-message-id="101"]'),
    ).toContainText("Pay");
    await expect(app.page.locator("#mail-detail .mail-subject")).toHaveText(
      "Synthetic invoice",
    );
    await expect(app.page.locator("#mail-detail")).toContainText(
      "Please review the synthetic invoice before Friday.",
    );
    await expect(app.page.locator("#mail-detail .triage-facet-tags")).toContainText(
      "Billing or account › Payment",
    );
    await expect(app.page.locator("#mail-detail .detail-flags")).toContainText(
      "Pay",
    );

    await app.page.locator('.mail-row[data-message-id="102"]').click();
    await expect(app.page.locator("#mail-detail .mail-subject")).toHaveText(
      "Synthetic budget",
    );
    await expect(app.page.locator("#mail-detail")).toContainText(
      "The synthetic budget is ready for review.",
    );
  });

  test("Home reads six priority rows while Important keeps its own 50-item page", async ({ app }) => {
    const limits: string[] = [];
    const countFilters: string[] = [];
    app.page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname === "/api/priority") {
        const limit = url.searchParams.get("limit") || "";
        limits.push(limit);
        if (limit === "1") countFilters.push(url.searchParams.get("filter") || "");
      }
    });
    await mailMocks(app.page);
    await app.goto("/hub");
    await app.expectRoute("hub");
    // Home renders six rows, so it asks for six. The 50-row ranked page is
    // the Important screen's read, not a payload Home pays for.
    await expect.poll(() => limits.includes("6")).toBe(true);
    expect(limits).not.toContain("50");

    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    await expect(app.page.locator(".mail-row").first()).toBeVisible();
    // The Important list rejects a Home-sized page and asks for its own
    // full page instead of painting six rows and rebuilding them.
    await expect.poll(() => limits.includes("50")).toBe(true);
    // The page carries every filter's count from the same snapshot: one
    // read for the list and its chips, never a single-row page per chip.
    await expect(app.page.locator(".filter-chips .filter-count").first()).not.toHaveText("");
    expect(countFilters).toEqual([]);
  });

  test("Important pages the ranked snapshot and keeps focus on Load more", async ({
    app,
  }) => {
    const offsets: number[] = [];
    const items = Array.from({ length: 120 }, (_, index) =>
      priorityItem(2000 + index),
    );
    await mailMocks(app.page, {
      priorityPage: (filter, limit, offset) => {
        if (filter !== "all" || limit !== 50) return priorityView(filter);
        offsets.push(offset);
        const page = items.slice(offset, offset + limit);
        return {
          items: page,
          total: items.length,
          offset,
          available: items.length,
          has_more: offset + page.length < items.length,
          counts: {
            all: items.length,
            reply: 0,
            urgent: 0,
            security: 0,
            decision: 0,
            deadline: 0,
            workload: 0,
          },
          generated_at: AT,
          preview: true,
        };
      },
    });
    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    const rows = app.page.locator(".mail-list .mail-row");
    await expect(rows).toHaveCount(50);
    const more = app.page.locator(".pager");
    await expect(more).toContainText("50 of 120");
    const loadMore = more.getByRole("button", { name: "Load more" });
    await expect(loadMore).toBeVisible();
    await loadMore.focus();
    await app.page.keyboard.press("Enter");
    await expect(rows).toHaveCount(100);
    await expect(rows.nth(50)).toHaveAttribute("data-message-id", "2050");
    await expect(more).toContainText("100 of 120");
    await expect(loadMore).toBeFocused();
    const ids = await rows.evaluateAll((nodes) =>
      nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
    );
    expect(new Set(ids).size).toBe(100);
    expect(offsets).toEqual([0, 50]);
  });

  test("a completed sync refreshes an accumulated Important list", async ({ app }) => {
    await installFakeEventSource(app.page);
    const offsets: number[] = [];
    let items = Array.from({ length: 120 }, (_, index) =>
      priorityItem(5000 + index),
    );
    let generatedAt = AT;
    await mailMocks(app.page, {
      priorityPage: (filter, limit, offset) => {
        if (filter !== "all" || limit !== 50) return priorityView(filter);
        offsets.push(offset);
        const page = items.slice(offset, offset + limit);
        return {
          items: page,
          total: items.length,
          offset,
          available: items.length,
          has_more: offset + page.length < items.length,
          counts: {
            all: items.length,
            reply: 0,
            urgent: 0,
            security: 0,
            decision: 0,
            deadline: 0,
            workload: 0,
          },
          generated_at: generatedAt,
          preview: true,
        };
      },
    });
    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    const rows = app.page.locator(".mail-list .mail-row");
    const more = app.page.locator(".pager");
    await expect(rows).toHaveCount(50);
    const loadMore = more.getByRole("button", { name: "Load more" });
    await loadMore.focus();
    await app.page.keyboard.press("Enter");
    await expect(rows).toHaveCount(100);
    await expect(more).toContainText("100 of 120");

    // The sync stores a smaller, reranked snapshot while Important is away.
    // Its accumulated pages must not outlive the read they came from.
    await app.page.locator('[data-nav="hub"]').click();
    await app.expectRoute("hub");
    items = Array.from({ length: 90 }, (_, index) => priorityItem(6000 + index));
    generatedAt = "2026-10-25T12:00:00Z";
    await emitSse(app.page, "sync.finished", { totals: { written: 2 } });
    // Let the coalesced refresh window run while Important is away: the
    // cached mount, not an active-screen repaint, must restore the model.
    await app.page.waitForTimeout(1_200);
    await app.page.goBack();
    await app.expectRoute("mail");

    await expect(rows).toHaveCount(50);
    await expect(rows.first()).toHaveAttribute("data-message-id", "6000");
    await expect(app.page.locator('#topbar-meta .pill', { hasText: "90 messages" })).toBeVisible();
    await expect(more).toContainText("50 of 90");
    await expect(app.page.locator('.mail-row[data-message-id="5000"]')).toHaveCount(0);
    expect(offsets).toEqual([0, 50, 0]);
  });

  test("Important keeps paging after a cached remount", async ({ app }) => {
    await installFakeEventSource(app.page);
    const offsets: number[] = [];
    const items = Array.from({ length: 170 }, (_, index) =>
      priorityItem(4000 + index),
    );
    await mailMocks(app.page, {
      priorityPage: (filter, limit, offset) => {
        if (filter !== "all" || limit !== 50) return priorityView(filter);
        offsets.push(offset);
        const page = items.slice(offset, offset + limit);
        return {
          items: page,
          total: items.length,
          offset,
          available: items.length,
          has_more: offset + page.length < items.length,
          counts: {
            all: items.length,
            reply: 0,
            urgent: 0,
            security: 0,
            decision: 0,
            deadline: 0,
            workload: 0,
          },
          generated_at: AT,
          preview: true,
        };
      },
    });
    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    const rows = app.page.locator(".mail-list .mail-row");
    const more = app.page.locator(".pager");
    await expect(rows).toHaveCount(50);
    const loadMore = more.getByRole("button", { name: "Load more" });
    await loadMore.focus();
    await app.page.keyboard.press("Enter");
    await expect(rows).toHaveCount(100);
    await expect(rows.nth(50)).toHaveAttribute("data-message-id", "4050");
    // Mark the mounted wrapper so Back can prove it re-appended this node
    // instead of building a fresh one.
    await app.page.locator(".mail-screen").evaluate((node) => node.setAttribute("data-e2e-cached", "important"));

    // Back re-appends the cached Important node without rerunning its render.
    // Its paging model owns the loaded rows, so Load more must keep working.
    await app.page.locator('[data-nav="hub"]').click();
    await app.expectRoute("hub");
    await app.page.goBack();
    await app.expectRoute("mail");
    await expect(app.page.locator('.mail-screen[data-e2e-cached="important"]')).toHaveCount(1);
    await expect(rows).toHaveCount(100);
    await expect(more).toContainText("100 of 170");
    const restored = more.getByRole("button", { name: "Load more" });
    await expect(restored).toBeVisible();
    await restored.focus();
    await app.page.keyboard.press("Enter");
    await expect(rows).toHaveCount(150);
    await expect(rows.nth(100)).toHaveAttribute("data-message-id", "4100");
    await expect(more).toContainText("150 of 170");
    await expect(restored).toBeFocused();
    expect(offsets).toEqual([0, 50, 100]);
  });

  test("a cached Important older than the priority TTL re-reads on mount", async ({
    app,
  }) => {
    await installFakeStreamWithClock(app.page);
    const offsets: number[] = [];
    let items = Array.from({ length: 120 }, (_, index) =>
      priorityItem(4500 + index),
    );
    await mailMocks(app.page, {
      priorityPage: (filter, limit, offset) => {
        if (filter !== "all" || limit !== 50) return priorityView(filter);
        offsets.push(offset);
        const page = items.slice(offset, offset + limit);
        return {
          items: page,
          total: items.length,
          offset,
          available: items.length,
          has_more: offset + page.length < items.length,
          counts: {
            all: items.length,
            reply: 0,
            urgent: 0,
            security: 0,
            decision: 0,
            deadline: 0,
            workload: 0,
          },
          generated_at: AT,
          preview: true,
        };
      },
    });
    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    const rows = app.page.locator(".mail-list .mail-row");
    await expect(rows).toHaveCount(50);

    await app.page.locator('[data-nav="hub"]').click();
    await app.expectRoute("hub");
    items = Array.from({ length: 20 }, (_, index) => priorityItem(4700 + index));
    // Older than CACHE_TTL.priority (15 s) but newer than the 20 s route
    // window: the model itself, not a background route refresh, must expire.
    await app.page.clock.fastForward(15_500);
    await app.page.goBack();
    await app.expectRoute("mail");
    await expect(rows).toHaveCount(20);
    await expect(rows.first()).toHaveAttribute("data-message-id", "4700");
    expect(offsets).toEqual([0, 0]);
  });

  // A cached Important page can outlive the HTTP read it came from. Its
  // background revalidation must keep the painted rows on failure and offer
  // the retry in the pane; a toast alone loses the failure with the screen.
  test("a failed Important revalidation keeps the rows and offers a retry", async ({
    app,
  }) => {
    await installFakeStreamWithClock(app.page);
    const items = Array.from({ length: 50 }, (_, index) =>
      priorityItem(7000 + index),
    );
    let fail = false;
    await mailMocks(app.page, {
      priorityFail: () => fail,
      priorityPage: (filter, limit, offset) => {
        if (filter !== "all" || limit !== 50) return priorityView(filter);
        const page = items.slice(offset, offset + limit);
        return {
          items: page,
          total: items.length,
          offset,
          available: items.length,
          has_more: offset + page.length < items.length,
          counts: {
            all: items.length,
            reply: 0,
            urgent: 0,
            security: 0,
            decision: 0,
            deadline: 0,
            workload: 0,
          },
          generated_at: AT,
          preview: true,
        };
      },
    });
    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    const rows = app.page.locator(".mail-list .mail-row");
    const line = app.page.locator(".mail-list-pane .conversation-error");
    await expect(rows).toHaveCount(50);

    // A second render inside the model TTL but after the HTTP read expires:
    // its cache hit touches the model time, so the next render revalidates
    // over the network.
    await app.page.clock.fastForward(8_000);
    await app.page.locator(".mail-screen").evaluate((node) => {
      node.setAttribute("data-e2e-probe", "before");
    });
    await app.page.evaluate(() => {
      location.hash = "#/mail?mode=important&filter=all&probe=1";
    });
    await expect(app.page.locator('.mail-screen[data-e2e-probe="before"]')).toHaveCount(0);
    await expect(rows).toHaveCount(50);
    await app.page.clock.fastForward(9_000);

    fail = true;
    await app.page.evaluate(() => {
      location.hash = "#/mail?mode=important&filter=all&probe=2";
    });
    await expect(line).toBeVisible();
    await expect(line).toContainText("Could not refresh; showing the last list.");
    await expect(rows).toHaveCount(50);
    await expect(rows.first()).toHaveAttribute("data-message-id", "7000");

    fail = false;
    await line.getByRole("button", { name: "Try again" }).click();
    await expect(line).toBeHidden();
    await expect(rows).toHaveCount(50);
    await expect(rows.first()).toHaveAttribute("data-message-id", "7000");
  });

  test("Important labels the top-200 snapshot cap honestly", async ({ app }) => {
    const items = Array.from({ length: 200 }, (_, index) =>
      priorityItem(3000 + index),
    );
    await mailMocks(app.page, {
      priorityPage: (filter, limit, offset) => {
        if (filter !== "all" || limit !== 50) return priorityView(filter);
        const page = items.slice(offset, offset + limit);
        return {
          items: page,
          total: 250,
          offset,
          available: items.length,
          has_more: offset + page.length < items.length,
          counts: { all: 250, reply: 0, urgent: 0, security: 0, decision: 0, deadline: 0, workload: 0 },
          generated_at: AT,
          preview: true,
        };
      },
    });
    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    const more = app.page.locator(".pager");
    await expect(app.page.locator(".mail-list .mail-row")).toHaveCount(50);
    await expect(more).toContainText("50 of 200");
    await expect(more).toContainText("Top 200 of 250 matches are pageable.");
  });

  test("Mail rows paint before the full learning status resolves", async ({
    app,
  }) => {
    await mailMocks(app.page);
    let releaseLearning!: () => void;
    const learningGate = new Promise<void>((resolve) => {
      releaseLearning = resolve;
    });
    let learningSeen!: () => void;
    const seen = new Promise<void>((resolve) => {
      learningSeen = resolve;
    });
    await app.page.route("**/api/learning", async (route) => {
      learningSeen();
      await learningGate;
      await route.continue();
    });

    await app.goto("/mail");
    await seen;
    try {
      // The one gate read is enough to paint the list; the full learning
      // status must not hold the content behind it.
      await expect(app.page.locator(".mail-row").first()).toBeVisible();
    } finally {
      releaseLearning();
    }
    await app.expectRoute("mail");
    await expect(app.page.locator("#screen-title")).not.toBeEmpty();
    await expect(app.page.locator(".mail-row").first()).toBeVisible();
  });

  test("the gate answer wins over a stale learning status", async ({ app }) => {
    await mailMocks(app.page);
    await app.page.route("**/api/learning", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          status: { state: "blocked", stages: [] },
          open: false,
          preview: false,
        }),
      });
    });
    await app.page.route("**/api/gate", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ allowed: true, open: true, preview: false }),
      });
    });

    await app.goto("/hub");
    await app.expectRoute("hub");
    await expect(app.page.locator("#topbar-meta")).toContainText("Gate open");
    await app.page.locator('.sidebar [data-nav="mail"]').click();
    await app.expectRoute("mail");
    await expect(app.page.locator(".mail-row").first()).toBeVisible();
    // The learning view says closed; the gate is authoritative on every screen.
    await expect(app.page.locator("#topbar-meta")).toContainText("Gate open");
  });

  test("a locked gate paints the locked screen without mail rows", async ({
    app,
  }) => {
    await mailMocks(app.page);
    await app.page.route("**/api/gate", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          allowed: false,
          preview: false,
          status: {
            state: "stuck",
            stages: [
              {
                id: "mailboxes",
                state: "blocked",
                progress: { done: 0, total: 0, unit: "mailboxes" },
                blockers: [
                  { code: "no_mailbox_picked", actions: [{ id: "pick_mailboxes" }] },
                ],
              },
            ],
          },
        }),
      });
    });

    await app.goto("/mail");
    await expect(app.page.locator(".locked")).toBeVisible();
    await expect(app.page.locator(".mail-row")).toHaveCount(0);
  });

  test("a filter narrows the list and keeps the selected message readable", async ({
    app,
  }) => {
    await mailMocks(app.page);
    await app.goto("/mail?mode=important");
    await app.page
      .locator(".filter-chips button")
      .filter({ hasText: "Reply" })
      .click();

    await expect(app.page).toHaveURL(/filter=reply/);
    await expect(
      app.page.locator('.mail-row[data-message-id="101"]'),
    ).toBeVisible();
    await expect(
      app.page.locator('.mail-row[data-message-id="102"]'),
    ).toHaveCount(0);
    await expect(app.page.locator("#mail-detail .mail-subject")).toHaveText(
      "Synthetic invoice",
    );
  });

  test("All mail pages through the mailbox and filters by folder", async ({
    app,
  }) => {
    await mailMocks(app.page);
    // No mode: the Mail tab now opens on All mail.
    await app.goto("/mail");
    await app.expectRoute("mail");

    await expect(
      app.page.locator('.all-mail-row[data-message-id="101"]'),
    ).toBeVisible();
    // The first page carries one row and a cursor; the observer appends the
    // second page in place.
    await expect(
      app.page.locator('.all-mail-row[data-message-id="102"]'),
    ).toBeVisible();
    const folderGroup = app.page.getByRole("group", { name: "Folder filter" });
    const readGroup = app.page.getByRole("group", { name: "Read state" });
    await expect(folderGroup).toBeVisible();
    await expect(readGroup).toBeVisible();
    await expect(readGroup.getByRole("button")).toHaveCount(1);
    const inboxChip = folderGroup.getByRole("button", { name: "Inbox" });
    const sentChip = folderGroup.getByRole("button", { name: "Sent" });
    const allChip = folderGroup.getByRole("button", { name: "All" });
    // The read-state wrapper must not stretch its chip when the folder group
    // wraps internally: both groups keep natural chip height.
    const unreadBox = await readGroup
      .getByRole("button", { name: "Unread", exact: true })
      .boundingBox();
    const inboxBox = await inboxChip.boundingBox();
    expect(unreadBox).not.toBeNull();
    expect(inboxBox).not.toBeNull();
    if (unreadBox && inboxBox) {
      expect(Math.abs(unreadBox.height - inboxBox.height)).toBeLessThan(2);
    }
    await expect(inboxChip).toContainText("1");
    // The unfiltered fixture has two rows: one inbox and one sent. These
    // counts are discriminating: Inbox 1 alone is also true when the stale
    // unfiltered counts are shown after the unread toggle.
    await expect(allChip).toContainText("2");
    await expect(sentChip).toContainText("1");

    // Unread is one boolean toggle composed with the folder and sender
    // filters: on it keeps only unread rows, off restores every read state.
    const unreadChip = readGroup.getByRole("button", {
      name: "Unread",
      exact: true,
    });
    await expect(unreadChip).toHaveAttribute("aria-pressed", "false");
    // A native button activates with the keyboard; focus explicitly so the
    // assertion does not depend on the pre-existing chip re-render focus.
    await unreadChip.focus();
    await app.page.keyboard.press("Space");
    await expect(app.page).toHaveURL(/unread=true/);
    await expect(unreadChip).toHaveAttribute("aria-pressed", "true");
    await expect(
      app.page.locator('.all-mail-row[data-message-id="101"]'),
    ).toBeVisible();
    await expect(
      app.page.locator('.all-mail-row[data-message-id="101"] .unread-dot'),
    ).toBeVisible();
    await expect(
      app.page.locator('.all-mail-row[data-message-id="102"]'),
    ).toHaveCount(0);
    // The filtered population is one unread inbox row, so every folder
    // count must change: total 2 -> 1, Sent 1 -> no badge.
    await expect(inboxChip).toContainText("1");
    await expect(allChip).toContainText("1");
    await expect(sentChip.locator(".filter-count")).toHaveCount(0);

    // A selected row keeps the read state in its URL.
    await app.page.locator('.all-mail-row[data-message-id="101"]').click();
    await expect(app.page).toHaveURL(/id=101/);
    await expect(app.page).toHaveURL(/unread=true/);

    // The sender field is gone: the one shell search owns sender queries.
    await expect(
      app.page.getByLabel("Filter mail by sender address or domain"),
    ).toHaveCount(0);
    await expect(app.page.locator(".sender-chip")).toHaveCount(0);
    await expect(unreadChip).toHaveAttribute("aria-pressed", "true");
    await unreadChip.click();
    await expect(app.page).not.toHaveURL(/unread=/);
    await expect(unreadChip).toHaveAttribute("aria-pressed", "false");
    await expect(
      app.page.locator('.all-mail-row[data-message-id="101"]'),
    ).toBeVisible();
    // Turning the filter off restores the unfiltered counts.
    await expect(allChip).toContainText("2");
    await expect(sentChip).toContainText("1");

    await sentChip.click();
    await expect(app.page).toHaveURL(/role=sent/);
    await expect(
      app.page.locator('.all-mail-row[data-message-id="102"]'),
    ).toBeVisible();
    await expect(
      app.page.locator('.all-mail-row[data-message-id="101"]'),
    ).toHaveCount(0);

    await app.page.locator('.all-mail-row[data-message-id="102"]').click();
    await expect(app.page).toHaveURL(/id=102/);
    await expect(app.page.locator("#mail-detail .mail-subject")).toHaveText(
      "Synthetic budget",
    );

    // A sender route still works from a deep link or the one shell search;
    // there is no second sender field to fill.
    await allChip.click();
    await app.goto("/mail?mode=all&sender=demo%40example.test");
    await expect(app.page).toHaveURL(/sender=demo%40example.test/);
    await expect(
      app.page.locator('.all-mail-row[data-message-id="101"]'),
    ).toBeVisible();
    await expect(
      app.page.locator('.all-mail-row[data-message-id="102"]'),
    ).toHaveCount(0);
    await expect(inboxChip).toContainText("1");
    // The one shell search owns sender queries; All Mail shows the active
    // sender as a removable chip so a deep link is never an invisible filter.
    await expect(app.page.locator(".sender-chip")).toHaveCount(1);
    await expect(app.page.locator(".sender-chip")).toContainText(
      "demo@example.test",
    );
    // The selection keeps the sender in its URL.
    await app.page.locator('.all-mail-row[data-message-id="101"]').click();
    await expect(app.page).toHaveURL(/id=101/);
    await expect(app.page).toHaveURL(/sender=demo%40example.test/);

    // A sender with no mail gets its own empty copy and a clear action.
    await app.goto("/mail?mode=all&sender=nobody%40example.test");
    await expect(app.page).toHaveURL(/sender=nobody%40example.test/);
    await expect(
      app.page.locator("#main").getByText("No mail from this sender"),
    ).toBeVisible();
    await expect(app.page.locator(".all-mail-row")).toHaveCount(0);
    await app.page.locator(".empty button").click();
    await expect(app.page).not.toHaveURL(/sender=/);
    await expect(
      app.page.locator('.all-mail-row[data-message-id="101"]'),
    ).toBeVisible();

    // Unread composes with a folder: Sent holds no unread row, so the
    // read-state copy wins over the folder copy. Clearing unread keeps Sent.
    await unreadChip.click();
    await sentChip.click();
    await expect(app.page).toHaveURL(/role=sent/);
    await expect(app.page).toHaveURL(/unread=true/);
    await expect(
      app.page.locator("#main").getByText("No unread mail"),
    ).toBeVisible();
    await expect(app.page.locator(".all-mail-row")).toHaveCount(0);
    await expect(app.page.locator(".empty button")).toHaveText(
      "Clear unread filter",
    );
    await app.page.locator(".empty button").click();
    await expect(app.page).not.toHaveURL(/unread=/);
    await expect(app.page).toHaveURL(/role=sent/);
    await expect(
      app.page.locator('.all-mail-row[data-message-id="102"]'),
    ).toBeVisible();

    // The two labeled groups share one strip. At a narrow width the strip
    // wraps; it must never become a horizontal scroll pane, and every chip
    // must stay inside the viewport.
    await app.page.setViewportSize({ width: 420, height: 800 });
    await expect(folderGroup).toBeVisible();
    await expect(readGroup).toBeVisible();
    await unreadChip.scrollIntoViewIfNeeded();
    const stripBox = await app.page.locator(".filter-chips").boundingBox();
    const chipBox = await unreadChip.boundingBox();
    expect(stripBox).not.toBeNull();
    expect(chipBox).not.toBeNull();
    if (stripBox && chipBox) {
      expect(chipBox.x).toBeGreaterThanOrEqual(0);
      expect(chipBox.x + chipBox.width).toBeLessThanOrEqual(421);
    }
    const strip = await app.page
      .locator(".filter-chips")
      .evaluate((node) => ({
        client: node.clientWidth,
        scroll: node.scrollWidth,
        overflow: getComputedStyle(node).overflowX,
        height: node.getBoundingClientRect().height,
      }));
    expect(strip.scroll).toBeGreaterThan(strip.client);
    expect(strip.overflow).toBe("auto");
    expect(strip.height).toBeLessThan(60);
  });

  test("All mail paints a partial batch first, then appends the rest in order", async ({
    app,
  }) => {
    // The stale-cache branch below waits out the real 20 s freshness window.
    test.setTimeout(60_000);
    // Drive the stream from the test; the stale-cache branch advances the
    // page's clock instead of sleeping out the 20 s freshness window.
    await installFakeStreamWithClock(app.page);
    const senderRequests: Array<{
      role: string;
      sender: string;
      before: string;
      unread: boolean;
    }> = [];
    await mailMocks(app.page, {
      messagesPage: (role, before, sender, unread) => {
        senderRequests.push({ role, sender, before, unread });
        // A sender plus another folder is a different, empty population: the
        // synthetic digest rows all belong to the Inbox.
        if (sender && role !== "all") return emptyMessageListPage();
        if (unread) return progressiveUnreadMailPage(before);
        return sender ? progressiveSenderMailPage(before) : progressiveMailPage(before);
      },
    });
    // Keep prefetchCore waiting so this test observes the click's own first
    // paint instead of a screen warmed while Home was idle.
    let releaseBoot!: () => void;
    const bootGate = new Promise<void>((resolve) => {
      releaseBoot = resolve;
    });
    await app.page.route("**/api/boot", async (route) => {
      await bootGate;
      await route.fallback();
    });
    try {
      await app.goto("/hub");
      await app.expectRoute("hub");
      await observeFirstMailFrame(app.page);
      // A real click would hover the sidebar and could start the Mail
      // prefetch; dispatching the click keeps the first batch deterministic.
      await app.page
        .locator('.sidebar [data-nav="mail"]')
        .evaluate((node) => (node as HTMLElement).click());
      await app.expectRoute("mail");

      await expect(app.page.locator(".all-mail-row").first()).toBeVisible();
      const first = await firstMailFrame(app.page);
      expect(first).not.toBeNull();
      if (!first) throw new Error("the first Mail frame was not recorded");
      expect(first.count).toBeGreaterThanOrEqual(24);
      expect(first.count).toBeLessThan(100);
      expect(first.count).toBe(24);
      expect(first.visible).toBe(true);
      expect(first.id).toMatch(/^\d+$/);
      expect(first.textLength).toBeGreaterThan(0);

      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      const ids = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(ids).toEqual(
        Array.from({ length: 100 }, (_, index) => 1000 + index),
      );
      await expect(
        app.page.locator(".filter-chips button").filter({ hasText: "Inbox" }),
      ).toContainText("150");
      await expect(app.page.locator("#mail-detail .mail-subject")).toHaveText(
        "Synthetic message 1000",
      );

      // A Load more already in flight when the folder changes must not leave
      // the new filter's sentinel stuck on the old request's loading state.
      let delayedCursor = true;
      await app.page.route(
        (url) =>
          url.pathname === "/api/messages" &&
          !!url.searchParams.get("before"),
        async (route) => {
          if (delayedCursor) {
            delayedCursor = false;
            await new Promise((resolve) => setTimeout(resolve, 1_500));
          }
          await route.fallback();
        },
      );
      await clickLoadMore(app.page);
      await app.page
        .locator(".filter-chips button")
        .filter({ hasText: "Inbox" })
        .click();
      await expect(app.page).toHaveURL(/role=inbox/);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      await expect(app.page.locator(".pager")).not.toContainText(
        /Loading more/,
      );
      await expect(
        app.page.locator(".pager").getByRole("button", { name: "Load more" }),
      ).toBeVisible();
      await app.page.waitForTimeout(1_800);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      await expect(
        app.page.locator('.all-mail-row[data-message-id="1100"]'),
      ).toHaveCount(0);
      await expect(app.page.locator(".pager")).not.toContainText(
        /Loading more/,
      );
      await expect(
        app.page.locator(".pager").getByRole("button", { name: "Load more" }),
      ).toBeVisible();

      // Return to unfiltered All Mail for the existing 100 -> 150 branch.
      await app.page
        .locator(".filter-chips button")
        .filter({ hasText: "All" })
        .click();
      await expect(app.page).toHaveURL(/role=all/);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);

      await app.page.locator(".mail-list").evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await expect(app.page.locator(".all-mail-row")).toHaveCount(150);
      const finalIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(finalIds).toEqual(
        Array.from({ length: 150 }, (_, index) => 1000 + index),
      );

      // The same builder and Load more on a sender-filtered list: the first
      // page is 100 rows of the sender population, the cursor page appends
      // the remaining 50. There is no sender field any more; the route is the
      // one entry point the shell search and old links use.
      await app.goto("/mail?mode=all&sender=digest%40example.test");
      await expect(app.page).toHaveURL(/sender=digest%40example.test/);
      const senderMark = senderRequests.length;
      await expect(app.page.locator(".all-mail-row").first()).toBeVisible();
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      const senderIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(senderIds).toEqual(
        Array.from({ length: 100 }, (_, index) => 2000 + index),
      );
      await expect(
        app.page.locator(".filter-chips button").filter({ hasText: "Inbox" }),
      ).toContainText("150");

      // A different folder keeps the sender filter and renders its own
      // population; the Inbox rows are not reused for it.
      await app.page
        .locator(".filter-chips button")
        .filter({ hasText: "Sent" })
        .click();
      await expect(app.page).toHaveURL(/role=sent/);
      await expect(app.page).toHaveURL(/sender=digest%40example.test/);
      await expect(
        app.page.locator("#main").getByText("No mail from this sender"),
      ).toBeVisible();
      await expect(app.page.locator(".all-mail-row")).toHaveCount(0);

      // Back restores the cached Inbox pane and its own model: Load more is
      // painted and the observer is re-armed before the next page is
      // requested.
      await app.page.goBack();
      await expect(app.page).toHaveURL(/sender=digest%40example.test/);
      await expect(app.page).not.toHaveURL(/role=sent/);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      const restoredIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(restoredIds).toEqual(
        Array.from({ length: 100 }, (_, index) => 2000 + index),
      );
      await expect(
        app.page.locator(".pager").getByRole("button", { name: "Load more" }),
      ).toBeVisible();

      await app.page.locator(".mail-list").evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await expect(app.page.locator(".all-mail-row")).toHaveCount(150);
      const senderFinalIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(senderFinalIds).toEqual(
        Array.from({ length: 150 }, (_, index) => 2000 + index),
      );
      expect(new Set(senderFinalIds).size).toBe(150);

      // Past the 20 s route-cache window the cached Back restores and then
      // refreshes. The refresh must rebuild all 150 loaded rows from the
      // model, not just the first page, and keep the end state coherent.
      await app.page
        .locator(".filter-chips button")
        .filter({ hasText: "Sent" })
        .click();
      await expect(app.page).toHaveURL(/role=sent/);
      await app.page.clock.fastForward(20_500);
      await observeMailListMounts(app.page);
      await app.page.goBack();
      await expect(app.page).toHaveURL(/sender=digest%40example.test/);
      await expect(app.page.locator(".mail-list")).toHaveCount(1);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(150);
      await expect
        .poll(() => mailListMounts(app.page))
        .toBeGreaterThanOrEqual(2);
      // The refreshed node paints 24 rows first and defers the rest; wait for
      // that deferred batch before reading the IDs.
      await expect(app.page.locator(".all-mail-row")).toHaveCount(150);
      const refreshedIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(refreshedIds).toEqual(
        Array.from({ length: 150 }, (_, index) => 2000 + index),
      );
      expect(new Set(refreshedIds).size).toBe(150);
      await expect(
        app.page.locator(".pager").getByRole("button", { name: "Load more" }),
      ).toHaveCount(0);
      await expect(app.page.locator(".pager")).toContainText(
        "End of the mailbox",
      );

      // The filter request, the Sent page and the restored Load more all
      // carried the sender; the back sequence never asked for sender=.
      const backRequests = senderRequests.slice(senderMark);
      expect(backRequests.length).toBeGreaterThanOrEqual(2);
      expect(backRequests.every((entry) => entry.sender === "digest@example.test")).toBe(true);

      // A bare #/mail prefetch while a filtered All-Mail list is mounted must
      // not build the default destination: that render would replace
      // state.allMail and disconnect the mounted list's one observer. The
      // config event invalidates the route cache without refreshing Mail, so
      // the hover/focus is the only code that can rebuild the destination.
      await app.goto("/mail?mode=all&sender=other%40example.test");
      await expect(app.page).toHaveURL(/sender=other%40example.test/);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      const filteredIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(filteredIds).toEqual(
        Array.from({ length: 100 }, (_, index) => 2000 + index),
      );

      let barePrefetches = 0;
      app.page.on("request", (request) => {
        const url = new URL(request.url());
        if (url.pathname !== "/api/messages") return;
        if (
          url.searchParams.has("role") ||
          url.searchParams.has("sender") ||
          url.searchParams.has("before")
        ) {
          return;
        }
        barePrefetches += 1;
      });
      await emitSse(app.page, "config.changed", { keys: ["mail.sync"] });
      barePrefetches = 0;

      const mailNav = app.page.locator('[data-nav="mail"]');
      await mailNav.hover();
      await mailNav.focus();
      await app.page.waitForTimeout(2_000);

      expect(barePrefetches).toBe(0);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      const prefetchedIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(prefetchedIds).toEqual(
        Array.from({ length: 100 }, (_, index) => 2000 + index),
      );
      await clickLoadMore(app.page);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(150);
      const prefetchedFinalIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(prefetchedFinalIds).toEqual(
        Array.from({ length: 150 }, (_, index) => 2000 + index),
      );
      expect(barePrefetches).toBe(0);

      // A same-filter refresh that starts while Load more is in flight must
      // wait for that page and build from the settled model. Rebuilding from
      // the stale model paints "Loading more" on a new list, and the old
      // request then repaints only its detached sentinel.
      await app.goto("/mail?mode=all&sender=third%40example.test");
      await expect(app.page).toHaveURL(/sender=third%40example.test/);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);

      // The unread toggle keeps the sender: the first page is the 100 unread
      // rows of that sender, the cursor page appends the remaining 50, and
      // every cursor request carries unread=true.
      const unreadToggle = app.page
        .locator(".filter-chips button")
        .filter({ hasText: "Unread" });
      await unreadToggle.click();
      await expect(app.page).toHaveURL(/unread=true/);
      await expect(app.page).toHaveURL(/sender=third%40example.test/);
      await expect(unreadToggle).toHaveAttribute("aria-pressed", "true");
      await expect(app.page.locator(".sender-chip")).toHaveCount(1);
      await expect(app.page.locator(".sender-chip")).toContainText(
        "third@example.test",
      );
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      await expect(
        app.page.locator(".all-mail-row .unread-dot"),
      ).toHaveCount(100);
      const unreadIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(unreadIds).toEqual(
        Array.from({ length: 100 }, (_, index) => 3000 + index),
      );
      await app.page.locator(".mail-list").evaluate((node) => {
        node.scrollTop = node.scrollHeight;
      });
      await expect(app.page.locator(".all-mail-row")).toHaveCount(150);
      const unreadFinalIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(unreadFinalIds).toEqual(
        Array.from({ length: 150 }, (_, index) => 3000 + index),
      );
      expect(new Set(unreadFinalIds).size).toBe(150);
      const unreadRequests = senderRequests.filter((entry) => entry.unread);
      expect(unreadRequests.length).toBeGreaterThanOrEqual(2);
      expect(
        unreadRequests.every((entry) => entry.sender === "third@example.test"),
      ).toBe(true);
      expect(unreadRequests.some((entry) => entry.before !== "")).toBe(true);

      // Turning it off returns to the sender's every-read-state page.
      await unreadToggle.click();
      await expect(app.page).not.toHaveURL(/unread=/);
      await expect(app.page).toHaveURL(/sender=third%40example.test/);
      await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
      const thirdIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(thirdIds).toEqual(
        Array.from({ length: 100 }, (_, index) => 2000 + index),
      );

      let cursorStartedResolve!: () => void;
      const cursorStarted = new Promise<void>((resolve) => {
        cursorStartedResolve = resolve;
      });
      let releaseCursorResolve!: () => void;
      const cursorGate = new Promise<void>((resolve) => {
        releaseCursorResolve = resolve;
      });
      let gatedCursor = false;
      await app.page.route(
        (url) =>
          url.pathname === "/api/messages" &&
          !!url.searchParams.get("before"),
        async (route) => {
          if (!gatedCursor) {
            gatedCursor = true;
            cursorStartedResolve();
            await cursorGate;
          }
          await route.fallback();
        },
      );
      await observeMailListMounts(app.page);
      await expect(app.page.locator("#topbar-update")).toBeHidden();

      await clickLoadMore(app.page);
      await cursorStarted;
      await expect(app.page.locator(".pager")).toContainText(
        /Loading more/,
      );

      // dropped -> one 400 ms live resync -> refreshActiveRoute. With the fix
      // the refresh waits on morePromise, so the Updating indicator stays
      // visible until this test releases the cursor; without it a new
      // .mail-list mounts from the stale model.
      await emitSse(app.page, "dropped", {
        reason: "buffer_full",
        topics: { "sync.progress": 3 },
        total: 3,
      });
      await expect
        .poll(async () => {
          const updating = await app.page.locator("#topbar-update").isVisible();
          return updating || (await mailListMounts(app.page)) > 0;
        })
        .toBe(true);

      releaseCursorResolve();

      // The pre-release poll can be satisfied by the Updating indicator
      // alone. The settled page must now be mounted by the refresh itself,
      // not merely leave the old list in place.
      await expect.poll(() => mailListMounts(app.page)).toBeGreaterThan(0);

      await expect(app.page.locator(".all-mail-row")).toHaveCount(150);
      const refreshRaceIds = await app.page
        .locator(".all-mail-row")
        .evaluateAll((nodes) =>
          nodes.map((node) => Number((node as HTMLElement).dataset.messageId)),
        );
      expect(refreshRaceIds).toEqual(
        Array.from({ length: 150 }, (_, index) => 2000 + index),
      );
      expect(new Set(refreshRaceIds).size).toBe(150);
      await expect(app.page.locator(".pager")).not.toContainText(
        /Loading more/,
      );
      await expect(
        app.page.locator(".pager").getByRole("button", { name: "Load more" }),
      ).toHaveCount(0);
      await expect(app.page.locator(".pager")).toContainText(
        "End of the mailbox",
      );
      await expect(app.page.locator("#topbar-update")).toBeHidden();
      await expect(app.page.locator("#main .mail-list")).toHaveCount(1);
    } finally {
      releaseBoot();
    }
  });

  test("Deep-link selection in the deferred batch", async ({ app }) => {
    await mailMocks(app.page, {
      messagesPage: (_role, before) => progressiveMailPage(before),
    });
    await app.goto("/mail?id=1090");
    await app.expectRoute("mail");
    await expect(app.page.locator(".all-mail-row")).toHaveCount(100);
    const row = app.page.locator('.all-mail-row[data-message-id="1090"]');
    await expect(row).toHaveClass(/selected/);
    await expect(app.page.locator("#mail-detail .mail-subject")).toHaveText(
      "Synthetic message 1090",
    );
    await expect(app.page.locator("#mail-detail")).toContainText(
      "Synthetic body for message 1090.",
    );
  });

  test("All mail empty page shows no rows and no fake row", async ({ app }) => {
    await mailMocks(app.page, {
      messagesPage: () => emptyMessageListPage(),
    });
    await app.goto("/mail");
    await app.expectRoute("mail");
    await expect(
      app.page.locator("#main").getByText("No mail in this folder"),
    ).toBeVisible();
    await expect(app.page.locator(".all-mail-row")).toHaveCount(0);
  });

  test("All mail error page shows no rows and keeps the retry", async ({
    app,
  }) => {
    // Hold events pending so a learning invalidation cannot accidentally
    // turn this Back into a fresh render; the error screen itself must not
    // be cached.
    await app.page.route("**/api/events*", () => new Promise<void>(() => {}));
    await mailMocks(app.page);
    let messageRequests = 0;
    await app.page.route(
      (url) => url.pathname === "/api/messages",
      async (route) => {
        messageRequests += 1;
        await route.fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ error: "synthetic read failure" }),
        });
      },
    );
    await app.goto("/mail");
    await app.expectRoute("mail");
    await expect(
      app.page.locator("#main").getByRole("heading", { name: "Request failed" }),
    ).toBeVisible();
    await expect(app.page.locator(".all-mail-row")).toHaveCount(0);
    await expect(
      app.page.locator("#main").getByRole("button", { name: /Try again/i }),
    ).toBeVisible();

    // An error pane is not a cacheable Mail screen: leaving and coming Back
    // must issue a fresh request instead of restoring the stale failure.
    await app.page
      .locator('.sidebar [data-nav="hub"]')
      .evaluate((node) => (node as HTMLElement).click());
    await app.expectRoute("hub");
    await app.page.waitForTimeout(300);
    const beforeBack = messageRequests;
    await app.page.goBack();
    await app.expectRoute("mail");
    await expect(
      app.page.locator("#main").getByRole("heading", { name: "Request failed" }),
    ).toBeVisible();
    await expect.poll(() => messageRequests).toBeGreaterThan(beforeBack);

    await expect(
      app.page.locator("#main").getByRole("button", { name: /Try again/i }),
    ).toBeVisible();
    const beforeRetry = messageRequests;
    await app.page
      .locator("#main")
      .getByRole("button", { name: /Try again/i })
      .click();
    await expect.poll(() => messageRequests).toBeGreaterThan(beforeRetry);
    await expect(
      app.page.locator("#main").getByRole("heading", { name: "Request failed" }),
    ).toBeVisible();
  });

  test("HTML body keeps sender layout, compiles its CSS and hydrates inline images", async ({
    app,
  }) => {
    await mailMocks(app.page);
    const html = messageView(101);
    html.body_format = "html";
    html.body_inline_images = 1;
    html.body_html =
      '<table data-mail-table="data"><thead><tr><th>Quarter</th><th>Total</th></tr></thead>' +
      '<tbody><tr><td>A1</td><td>B1</td></tr></tbody></table>' +
      '<p class="cta">Open the report</p>' +
      '<p><span class="mail-inline-asset" data-inline="0" data-inline-v="abc123" role="img" aria-label="signature logo">signature logo</span></p>' +
      '<p><span class="mail-inline-asset" data-inline="1" data-inline-v="missing" role="img" aria-label="missing logo">missing logo</span></p>';
    html.body_css = ".cta{background-color:#1e5a46;color:#ffffff;padding:8px 12px;}";
    await app.page.route(
      (url) => url.pathname === "/api/messages/101",
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(html),
        });
      },
    );
    const attachmentRequests: Array<{ url: string; authorization: string }> = [];
    await app.page.route(
      (url) => url.pathname === "/api/messages/101/attachments/0",
      async (route) => {
        const request = route.request();
        attachmentRequests.push({
          url: request.url(),
          authorization: request.headers()["authorization"] || "",
        });
        await route.fulfill({
          status: 200,
          contentType: "image/gif",
          body: Buffer.from(
            "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7",
            "base64",
          ),
        });
      },
    );

    await app.goto("/mail?id=101");
    await app.expectRoute("mail");
    const frame = app.page.frameLocator("#mail-detail iframe.mail-frame");
    await expect(frame.locator("table[data-mail-table='data']")).toBeVisible();
    await expect(frame.getByText("Open the report")).toBeVisible();
    // The compiled sender sheet is linked as a blob after mail.css; the
    // fragment itself still carries no style attribute.
    await expect(frame.locator(".cta")).toHaveCSS(
      "background-color",
      "rgb(30, 90, 70)",
    );
    const styles = await frame
      .locator("link[rel='stylesheet']")
      .evaluateAll((nodes) =>
        nodes.map((node) => (node as HTMLLinkElement).getAttribute("href") || ""),
      );
    expect(styles.some((href) => href.startsWith("blob:"))).toBe(true);
    const image = frame.locator("img[alt='signature logo']");
    await expect(image).toBeVisible();
    expect(
      await image.evaluate((node) => (node as HTMLImageElement).naturalWidth),
    ).toBeGreaterThan(0);
    expect(attachmentRequests).toHaveLength(1);
    expect(attachmentRequests[0].authorization).toMatch(/^Bearer /);
    expect(attachmentRequests[0].url).not.toContain("token=");
    // A part with no stored bytes keeps a readable placeholder instead of a
    // broken image icon.
    await expect(
      frame.getByText("Inline image unavailable", { exact: true }),
    ).toBeVisible();

    // A 600px newsletter table still fits the narrow reader: no horizontal
    // scrollbar inside the message document.
    await app.page.setViewportSize({ width: 390, height: 800 });
    const overflow = await frame.locator("body").evaluate(
      (body) => body.scrollWidth - body.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });

  test("reopening a cached message paints its body once", async ({ app }) => {
    await mailMocks(app.page);
    const html = messageView(101);
    html.body_format = "html";
    html.body_inline_images = 1;
    html.body_html =
      '<p>Quarterly report</p>' +
      '<p><span class="mail-inline-asset" data-inline="0" data-inline-v="abc123" role="img" aria-label="signature logo">signature logo</span></p>';
    await app.page.route(
      (url) => url.pathname === "/api/messages/101",
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(html),
        });
      },
    );
    await app.page.route(
      (url) => url.pathname === "/api/messages/101/attachments/0",
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "image/gif",
          body: Buffer.from(
            "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7",
            "base64",
          ),
        });
      },
    );

    await app.goto("/mail?id=101");
    await app.expectRoute("mail");
    const frame = app.page.frameLocator("#mail-detail iframe.mail-frame");
    await expect(frame.getByText("Quarterly report")).toBeVisible();
    await expect(frame.locator("img[alt='signature logo']")).toBeVisible();

    // Count document paints rather than asset reads: the inline image is
    // fetched once per message even when the iframe is recreated.
    await app.page.evaluate(() => {
      const proto = HTMLIFrameElement.prototype as unknown as {
        srcdoc: string;
      };
      const desc = Object.getOwnPropertyDescriptor(
        HTMLIFrameElement.prototype,
        "srcdoc",
      )!;
      (window as unknown as { srcdocSets: number }).srcdocSets = 0;
      Object.defineProperty(HTMLIFrameElement.prototype, "srcdoc", {
        configurable: true,
        get() { return desc.get!.call(this); },
        set(value: string) {
          (window as unknown as { srcdocSets: number }).srcdocSets += 1;
          desc.set!.call(this, value);
        },
      });
    });

    // A different message in the same screen, then back within the cache
    // window: the preview is painted from the cached message and triage
    // objects, and the final read returns those same objects. A second paint
    // would recreate the iframe document.
    await app.page.locator('.mail-row[data-message-id="102"]').click();
    await expect(app.page.locator("#mail-detail .mail-subject")).toHaveText(
      "Synthetic budget",
    );
    await app.page.evaluate(() => {
      (window as unknown as { srcdocSets: number }).srcdocSets = 0;
    });
    await app.page.locator('.mail-row[data-message-id="101"]').click();
    await expect(frame.getByText("Quarterly report")).toBeVisible();
    await app.page.waitForTimeout(500);
    const paints = await app.page.evaluate(
      () => (window as unknown as { srcdocSets: number }).srcdocSets,
    );
    expect(paints).toBe(1);
  });

  test("search opens the hit in the same viewer", async ({ app }) => {
    await mailMocks(app.page);
    await app.goto("/mail?mode=search&q=invoice");
    await app.expectRoute("mail");

    await expect(app.page.locator('.search-result[data-id="101"]')).toBeVisible();
    await expect(app.page.locator("#mail-detail .mail-subject")).toHaveText(
      "Synthetic invoice",
    );
    await expect(app.page.locator("#mail-detail")).toContainText(
      "Please review the synthetic invoice before Friday.",
    );
  });

  test("the shell search is the only mail search", async ({ app }) => {
    await mailMocks(app.page);
    // Make message 101 HTML before any screen reads it: the client caches a
    // message for 60 s, and a hash navigation does not reset the JS heap.
    const html = messageView(101);
    html.body_format = "html";
    html.body_html = '<p>Body for the search shortcut probe. <a href="https://example.com/">link</a></p>';
    await app.page.route(
      (url) => url.pathname === "/api/messages/101",
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(html),
        });
      },
    );
    for (const route of ["/hub", "/mail", "/companies", "/settings"]) {
      await app.goto(route);
      await expect(app.page.locator("#global-search-input")).toBeVisible();
    }
    // No second visible search field anywhere.
    for (const route of ["/companies", "/settings", "/logs"]) {
      await app.goto(route);
      await expect(
        app.page.locator('#main input[type="search"]:visible'),
      ).toHaveCount(0);
    }

    // The Mail screen has no second Search tab and no second query box.
    await app.goto("/mail?mode=search&q=invoice");
    await expect(app.page.locator(".mail-tabs")).not.toContainText("Search");
    await expect(app.page.locator(".mail-search-card input")).toHaveCount(0);

    // The shell box suggests local hits and Enter opens the full result.
    await app.goto("/hub");
    await app.page.locator("#global-search-input").fill("invoice");
    await expect(app.page.locator(".topbar-suggest").first()).toBeVisible();
    await app.page.locator("#global-search-input").press("Enter");
    await expect(app.page).toHaveURL(/#\/mail\?mode=search&q=invoice/);
    await expect(app.page.locator('.search-result[data-id="101"]')).toBeVisible();

    // "/" also works while the message reader owns focus: its sandboxed
    // iframe never bubbles a key event to the shell by itself. Start a fresh
    // document so no earlier text-body response for 101 is reused.
    await app.page.goto("about:blank");
    await app.goto("/mail?id=101");
    const frame = app.page.frameLocator("#mail-detail iframe.mail-frame");
    await expect(frame.locator("body")).toBeVisible();
    await frame.locator("a").focus();
    await expect
      .poll(() => app.page.evaluate(() => document.activeElement?.tagName || ""))
      .toBe("IFRAME");
    await frame.locator("a").press("/");
    await expect(app.page.locator("#global-search-input")).toBeFocused();

    // "/" focuses the one search box from anywhere outside a text field.
    await app.goto("/companies");
    await app.page.locator("body").press("/");
    await expect(app.page.locator("#global-search-input")).toBeFocused();

    // A numeric-keypad divide reports key "Divide" on some layouts.
    await app.page.locator("#global-search-input").blur();
    await app.page.evaluate(() => {
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Divide", code: "NumpadDivide", bubbles: true }),
      );
    });
    await expect(app.page.locator("#global-search-input")).toBeFocused();

    // The shortcut never hijacks a real text field: "/" is typed there.
    await app.goto("/ask");
    const composer = app.page.locator("#ask-input");
    await composer.focus();
    await composer.press("/");
    await expect(app.page.locator("#global-search-input")).not.toBeFocused();
    await expect(composer).toHaveValue("/");
  });
});

// A route-switch button owns a stable focus key. Keyboard activation must
// land on the same segment after the Mail screen rebuild, not on #main.
test("a keyboard Mail tab switch returns focus to the active tab", async ({ app }) => {
  await mailMocks(app.page);
  await app.goto("/mail?mode=important");
  await app.expectRoute("mail");

  const tabs = app.page.locator(".mail-tabs");
  const all = tabs.getByRole("button", { name: "All mail", exact: true });
  await all.focus();
  await app.page.keyboard.press("Enter");
  await expect(app.page).toHaveURL(/mode=all/);
  await expect(all).toHaveAttribute("aria-current", "page");
  await expect(all).toBeFocused();
});

// A filter chip owns a stable focus key too. Activating one rebuilds the
// whole All mail screen, so focus must land on the chip that is now active
// instead of falling back to #main.
test("a filter chip keeps focus on the active chip after the rebuild", async ({
  app,
}) => {
  await mailMocks(app.page);
  await app.goto("/mail");
  await app.expectRoute("mail");
  await expect(
    app.page.locator('.all-mail-row[data-message-id="101"]'),
  ).toBeVisible();

  const folders = app.page.getByRole("group", { name: "Folder filter" });
  const sent = folders.getByRole("button", { name: "Sent" });
  await sent.focus();
  await app.page.keyboard.press("Enter");
  await expect(app.page).toHaveURL(/role=sent/);
  await expect(sent).toHaveAttribute("aria-pressed", "true");
  await expect(sent).toBeFocused();

  // The read-state toggle is a different group but the same contract.
  const unread = app.page
    .getByRole("group", { name: "Read state" })
    .getByRole("button", { name: "Unread", exact: true });
  await unread.focus();
  await app.page.keyboard.press("Space");
  await expect(app.page).toHaveURL(/unread=true/);
  await expect(unread).toBeFocused();
});
