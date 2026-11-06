import {
  AT,
  hubCertificationView,
  learningView,
  messageView,
  questionsView,
  triageResult,
  statusView
} from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

// internal/triage.PageRow: the list projection, never the full result.
function pageRow(id: number, overrides: Record<string, unknown> = {}) {
  return {
    message_id: id,
    answered: 0,
    abstained: 0,
    first_question: "",
    first_value: { type: "noul", number: 0 },
    missing: 0,
    stale: false,
    decided_at: AT,
    escalate: false,
    preview: false,
    ...overrides,
  };
}

const fullyAbstained = 501;
const mixed = 502;
const fullyAnswered = 503;

// A one-page triage read whose ids carry the server order. Numeric result
// keys serialize in ascending order, so the ids are deliberately the only
// newest-first signal.
function orderedTriagePage(ids: number[]): Record<string, unknown> {
  const results: Record<string, ReturnType<typeof pageRow>> = {};
  for (const id of [...ids].reverse()) results[id] = pageRow(id);
  return {
    total: ids.length,
    limit: 50,
    offset: 0,
    routed: true,
    preview: false,
    ids,
    results,
  };
}

type TriageMockOptions = {
  coverage?: {
    eligible: number;
    fresh: number;
    incomplete: number;
    stale: number;
    failed: number;
    unjudged: number;
  };
  summaryStatus?: number;
  triagePage?: Record<string, unknown>;
  detail?: (id: number) => Record<string, unknown>;
};

async function triageMocks(
  page: Parameters<typeof mockApi>[0],
  options: TriageMockOptions = {},
) {
  await mockApi(page, [
    {
      method: "GET",
      path: "/api/learning",
      body: learningView({ open: true, preview: false }),
    },
    {
      method: "GET",
      path: "/api/status",
      body: statusView(),
    },
    { method: "GET", path: "/api/questions", body: questionsView() },
    {
      method: "GET",
      path: "/api/triage/summary",
      handler: async ({ route }) => {
        await fulfillJson(
          route,
          {
            coverage: options.coverage ?? {
              eligible: 3,
              fresh: 3,
              incomplete: 0,
              stale: 0,
              failed: 0,
              unjudged: 0,
            },
            preview: false,
          },
          options.summaryStatus ?? 200,
        );
      },
    },
    {
      method: "GET",
      path: "/api/triage",
      body: options.triagePage ?? {
        total: 3,
        limit: 50,
        offset: 0,
        routed: true,
        preview: false,
        results: {
          [fullyAbstained]: pageRow(fullyAbstained, { abstained: 17 }),
          [mixed]: pageRow(mixed, {
            answered: 5,
            abstained: 2,
            missing: 3,
            first_question: "reply_needed",
            first_value: { type: "noul", number: 0.9, decided: "yes" },
          }),
          [fullyAnswered]: pageRow(fullyAnswered, {
            answered: 17,
            first_question: "reply_needed",
            first_value: { type: "noul", number: 0.9, decided: "yes" },
          }),
        },
      },
    },
    {
      method: "GET",
      path: /^\/api\/triage\/\d+$/,
      handler: async ({ route, url }) => {
        const id = Number(url.pathname.split("/").pop());
        await fulfillJson(route, options.detail
          ? options.detail(id)
          : {
              message_id: id,
              values: {},
              stale: false,
              escalate: false,
              preview: false,
            });
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
  ]);
}

test.describe("Recent triage rows", () => {
  test("a row shows answered, abstained and missing apart", async ({ app }) => {
    await triageMocks(app.page);
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");

    // A fully uncertified row has no answered value at all: the count names
    // the abstentions and the subject keeps the temporary-value placeholder
    // instead of painting an abstained first value.
    const abstainedRow = app.page.locator(
      `.triage-row[data-message-id="${fullyAbstained}"]`,
    );
    await expect(abstainedRow).toBeVisible();
    await expect(abstainedRow.locator(".mail-row-snippet")).toHaveText(
      "0 answered · 17 abstained",
    );
    await expect(abstainedRow.locator(".mail-row-subject")).toHaveText("Not set");

    // A partly answered row keeps all three counts in their order.
    const mixedRow = app.page.locator(
      `.triage-row[data-message-id="${mixed}"]`,
    );
    await expect(mixedRow.locator(".mail-row-snippet")).toHaveText(
      "5 answered · 2 abstained · 3 missing",
    );
    await expect(mixedRow.locator(".mail-row-subject")).toContainText(
      "Does this need a reply?",
    );

    // A fully answered row is unchanged: no abstention suffix, and the
    // answered first value still paints the subject.
    const answeredRow = app.page.locator(
      `.triage-row[data-message-id="${fullyAnswered}"]`,
    );
    await expect(answeredRow.locator(".mail-row-snippet")).toHaveText(
      "17 answered",
    );
    await expect(answeredRow.locator(".mail-row-subject")).toContainText(
      "Does this need a reply?",
    );
  });

  test("a row counts automatic rule answers apart and never as certified", async ({
    app,
  }) => {
    await triageMocks(app.page, {
      triagePage: {
        total: 1,
        limit: 50,
        offset: 0,
        routed: true,
        preview: false,
        ids: [mixed],
        results: {
          [mixed]: pageRow(mixed, {
            answered: 0,
            abstained: 1,
            automatic: 1,
            first_question: "category",
            first_value: { type: "choice", choice: "work", rule: "cat_work" },
          }),
        },
      },
    });
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");
    const row = app.page.locator(`.triage-row[data-message-id="${mixed}"]`);
    await expect(row.locator(".mail-row-snippet")).toHaveText(
      "0 answered · 1 abstained · 1 automatic, uncertified",
    );
    // The value is shown, but the row says the route did not certify it.
    await expect(row.locator(".mail-row-subject")).toContainText(
      "Work update (uncertified)",
    );
  });

  test("Model details names automatic answers and keeps their provenance", async ({
    app,
  }) => {
    await triageMocks(app.page, {
      detail: (id) => ({
        message_id: id,
        values: {
          category: { type: "choice", choice: "work", rule: "cat_work" },
          is_question: { type: "noul", number: 1, decided: "yes", rule: "question_mark" },
          urgency: { type: "score", abstain: "not_certified" },
        },
        stale: false,
        escalate: false,
        preview: false,
      }),
    });
    await app.goto(`/mail?mode=recent&id=${mixed}`);
    await app.expectRoute("mail");
    await app.page.getByText("Model details", { exact: true }).click();
    const details = app.page.locator(".triage-model-details");
    await expect(details).toContainText(
      "2 answers come from automatic rules; the route has not certified them.",
    );
    const category = details.locator(".triage-model-row").filter({
      hasText: "What kind of mail is this?",
    });
    await expect(category).toContainText("automatic rule · uncertified");
    const question = details.locator(".triage-model-row").filter({
      hasText: "Does it ask a question?",
    });
    await expect(question).toContainText("automatic rule");
    await expect(question).toContainText("Yes");
    await expect(question).not.toContainText("probability");
    // A question no rule can answer says why, not just "no usable answer".
    const urgency = details.locator(".triage-model-row").filter({
      hasText: "How urgent is it?",
    });
    await expect(urgency).toContainText("Not certified yet");
  });

  test("the server's ids order the rows and select the first", async ({ app }) => {
    await triageMocks(app.page, { triagePage: orderedTriagePage([1002, 1001]) });
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");

    const rows = app.page.locator(".mail-list .triage-row");
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0)).toHaveAttribute("data-message-id", "1002");
    await expect(rows.nth(1)).toHaveAttribute("data-message-id", "1001");
    await expect(rows.first()).toHaveClass(/selected/);
    await expect(app.page.locator("#mail-detail")).toContainText(
      "Synthetic message 1002",
    );
  });

  test("a selected id missing from the server order falls back to the first row", async ({
    app,
  }) => {
    await triageMocks(app.page, { triagePage: orderedTriagePage([1002, 1001]) });
    await app.goto("/mail?mode=recent&id=9999");
    await app.expectRoute("mail");

    const rows = app.page.locator(".mail-list .triage-row");
    await expect(rows.first()).toHaveAttribute("data-message-id", "1002");
    await expect(rows.first()).toHaveClass(/selected/);
    await expect(app.page.locator("#mail-detail")).toContainText(
      "Synthetic message 1002",
    );
  });

  test("without server ids the rows keep the descending fallback", async ({
    app,
  }) => {
    await triageMocks(app.page);
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");

    const rows = app.page.locator(".mail-list .triage-row");
    await expect(rows).toHaveCount(3);
    await expect(rows.nth(0)).toHaveAttribute(
      "data-message-id",
      String(fullyAnswered),
    );
    await expect(rows.nth(1)).toHaveAttribute("data-message-id", String(mixed));
    await expect(rows.nth(2)).toHaveAttribute(
      "data-message-id",
      String(fullyAbstained),
    );
  });

  test("the route pill counts certification and the coverage disclosure says Judged", async ({
    app,
  }) => {
    let hub = hubCertificationView(17, 17);
    let hubHold: Promise<void> = Promise.resolve();
    let releaseHub: (() => void) | null = null;
    let hubSeen: (() => void) | null = null;
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/hub",
        handler: async ({ route }) => {
          const seen = hubSeen;
          hubSeen = null;
          seen?.();
          await hubHold;
          await fulfillJson(route, hub);
        },
      },
    ]);
    await triageMocks(app.page);

    const openRecent = async () => {
      // Direct load: the route itself waits for the delayed Hub snapshot.
      hubHold = new Promise<void>((resolve) => {
        releaseHub = resolve;
      });
      const started = new Promise<void>((resolve) => {
        hubSeen = resolve;
      });
      await app.page.goto("about:blank");
      await app.goto("/mail?mode=recent");
      await started;
      releaseHub?.();
      await hubHold;
      await app.expectRoute("mail");
      await expect(app.page.locator(".triage-row").first()).toBeVisible();
    };

    const topbar = app.page.locator("#topbar-meta");
    await openRecent();
    const certifiedPill = topbar.locator(".pill", { hasText: "Certified route" });
    await expect(certifiedPill).toHaveClass("pill accent");
    await expect(topbar).toContainText("Judged 3/3");
    await expect(topbar.locator(".pill", { hasText: "Judged 3/3" })).toHaveClass(
      "pill done",
    );
    const certifiedBackground = await certifiedPill.evaluate(
      (node) => getComputedStyle(node).backgroundColor,
    );

    await app.page.locator(".triage-coverage summary").click();
    const coverage = app.page.locator(".triage-coverage");
    await expect(coverage).toContainText("Judged rows");
    await expect(coverage).toContainText("Judged");
    await expect(coverage).toContainText(
      "Judged rows have a stored answer or abstention; certification is tracked separately.",
    );

    hub = hubCertificationView(1, 17);
    await openRecent();
    const partialPill = topbar.locator(".pill", { hasText: "1 of 17 certified" });
    await expect(partialPill).toHaveClass("pill warn");
    expect(await partialPill.evaluate((node) => getComputedStyle(node).backgroundColor)).not.toBe(
      certifiedBackground,
    );
    await expect(topbar).not.toContainText("Certified route");

    hub = hubCertificationView(0, 0);
    await openRecent();
    await expect(topbar).toContainText("Certification unknown");
    await expect(topbar).not.toContainText("Certified route");
  });

  test("partial coverage is a warning and complete coverage is done", async ({
    app,
  }) => {
    const coverage = {
      eligible: 17,
      fresh: 3,
      incomplete: 0,
      stale: 0,
      failed: 0,
      unjudged: 14,
    };
    await triageMocks(app.page, { coverage });
    await app.page.goto("about:blank");
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");
    await expect(app.page.locator(".triage-row").first()).toBeVisible();
    await expect(
      app.page.locator("#topbar-meta .pill", { hasText: "Judged 3/17" }),
    ).toHaveClass("pill warn");

    coverage.fresh = 17;
    coverage.unjudged = 0;
    await app.page.goto("about:blank");
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");
    await expect(
      app.page.locator("#topbar-meta .pill", { hasText: "Judged 17/17" }),
    ).toHaveClass("pill done");
  });

  test("a failed coverage summary keeps the list and omits unknown coverage", async ({
    app,
  }) => {
    await triageMocks(app.page, { summaryStatus: 500 });
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");

    await expect(app.page.locator(".triage-row")).toHaveCount(3);
    await expect(app.page.locator(".mail-list-pane")).not.toContainText(
      "Request failed",
    );
    await expect(app.page.locator("#topbar-meta")).not.toContainText("Judged");
    await expect(app.page.locator(".triage-coverage")).toHaveCount(0);
    await expect(app.page.locator(".triage-coverage-unavailable")).toHaveText(
      "Coverage is unavailable until the triage summary can be read.",
    );
  });

  test("the preview pill wins over a full certification count", async ({ app }) => {
    await triageMocks(app.page);
    await mockApi(app.page, [
      { method: "GET", path: "/api/hub", body: hubCertificationView(17, 17) },
      {
        method: "GET",
        path: "/api/learning",
        body: learningView({ open: false, preview: true }),
      },
      {
        method: "GET",
        path: "/api/gate",
        body: { allowed: true, open: false, preview: true },
      },
    ]);

    await app.goto("/hub");
    await app.expectRoute("hub");
    await app.page.locator('[data-nav="mail"]').click();
    await app.expectRoute("mail");
    await app.page.getByRole("button", { name: "Recent", exact: true }).click();
    await expect(app.page.locator(".triage-row").first()).toBeVisible();

    const topbar = app.page.locator("#topbar-meta");
    await expect(topbar).toContainText("Uncertified preview");
    await expect(topbar).not.toContainText("Certified route");
  });
});

// pagedTriageWorld is a mutable recent-mail snapshot: an event test changes
// its population between visits, exactly like a completed triage run.
type PagedTriageWorld = {
  total: number;
  top: number;
  answered: number;
  offsets: number[];
};

function triagePageResults(
  world: PagedTriageWorld,
  offset: number,
  limit: number,
): Record<string, ReturnType<typeof pageRow>> {
  const results: Record<string, ReturnType<typeof pageRow>> = {};
  const last = Math.min(offset + limit, world.total);
  for (let index = offset; index < last; index += 1) {
    const id = world.top - index;
    results[id] = pageRow(id, { answered: world.answered });
  }
  return results;
}

async function pagedTriageMocks(
  page: Parameters<typeof mockApi>[0],
  world: PagedTriageWorld,
): Promise<void> {
  await mockApi(page, [
    {
      method: "GET",
      path: "/api/learning",
      body: learningView({ open: true, preview: false }),
    },
    {
      method: "GET",
      path: "/api/status",
      body: statusView(),
    },
    { method: "GET", path: "/api/questions", body: questionsView() },
    {
      method: "GET",
      path: "/api/triage/summary",
      body: {
        coverage: {
          eligible: world.total,
          fresh: world.total,
          incomplete: 0,
          stale: 0,
          failed: 0,
          unjudged: 0,
        },
        preview: false,
      },
    },
    {
      method: "GET",
      path: "/api/triage",
      handler: async ({ route, url }) => {
        const offset = Number(url.searchParams.get("offset") || 0);
        world.offsets.push(offset);
        const results = triagePageResults(world, offset, 50);
        await fulfillJson(route, {
          total: world.total,
          limit: 50,
          offset,
          routed: true,
          preview: false,
          ids: Object.keys(results)
            .map(Number)
            .sort((a, b) => b - a),
          results,
        });
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
    {
      method: "GET",
      path: /^\/api\/messages\/\d+$/,
      handler: async ({ route, url }) => {
        const id = Number(url.pathname.split("/").pop());
        await fulfillJson(route, messageView(id));
      },
    },
  ]);
}

test.describe("Recent paging models", () => {
  test("a completed triage run refreshes an accumulated Recent list", async ({
    app,
  }) => {
    await installFakeEventSource(app.page);
    const world: PagedTriageWorld = {
      total: 120,
      top: 3120,
      answered: 1,
      offsets: [],
    };
    await pagedTriageMocks(app.page, world);
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");
    const rows = app.page.locator(".mail-list .triage-row");
    const tools = app.page.locator(".mail-list-tools");
    await expect(rows).toHaveCount(50);
    const loadMore = tools.getByRole("button", { name: "Load more" });
    await loadMore.focus();
    await app.page.keyboard.press("Enter");
    await expect(rows).toHaveCount(100);
    await expect(tools).toContainText("100 shown · 20 not loaded");

    // The run reclassifies the population while Recent is away. The
    // accumulated page model must not outlive the read it came from.
    await app.page.locator('[data-nav="hub"]').click();
    await app.expectRoute("hub");
    world.top = 3300;
    world.total = 50;
    world.answered = 3;
    await emitSse(app.page, "triage.finished", { decided: 2 });
    // Let the coalesced refresh window run while Recent is away: the cached
    // mount, not an active-screen repaint, must restore the model.
    await app.page.waitForTimeout(1_200);
    await app.page.goBack();
    await app.expectRoute("mail");

    await expect(rows).toHaveCount(50);
    await expect(rows.first()).toHaveAttribute("data-message-id", "3300");
    await expect(rows.first()).toContainText("3 answered");
    await expect(tools).toContainText("50 shown · 0 not loaded");
    await expect(app.page.locator('.triage-row[data-message-id="3021"]')).toHaveCount(0);
    expect(world.offsets).toEqual([0, 50, 0]);
  });

  test("Recent keeps paging after a cached remount and restores focus", async ({
    app,
  }) => {
    const world: PagedTriageWorld = {
      total: 170,
      top: 3120,
      answered: 1,
      offsets: [],
    };
    await pagedTriageMocks(app.page, world);
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");
    const rows = app.page.locator(".mail-list .triage-row");
    const tools = app.page.locator(".mail-list-tools");
    await expect(rows).toHaveCount(50);
    const loadMore = tools.getByRole("button", { name: "Load more" });
    await loadMore.focus();
    await app.page.keyboard.press("Enter");
    await expect(rows).toHaveCount(100);
    await expect(loadMore).toBeFocused();
    await app.page.locator(".mail-screen").evaluate((node) => node.setAttribute("data-e2e-cached", "recent"));

    // Back re-appends the cached Recent node without rerunning renderRecent.
    // The page model stays with it, so the next page still loads.
    await app.page.locator('[data-nav="hub"]').click();
    await app.expectRoute("hub");
    await app.page.goBack();
    await app.expectRoute("mail");
    await expect(app.page.locator('.mail-screen[data-e2e-cached="recent"]')).toHaveCount(1);
    await expect(rows).toHaveCount(100);
    await expect(tools).toContainText("100 shown · 70 not loaded");
    const restored = tools.getByRole("button", { name: "Load more" });
    await expect(restored).toBeVisible();
    await restored.focus();
    await app.page.keyboard.press("Enter");
    await expect(rows).toHaveCount(150);
    await expect(tools).toContainText("150 shown · 20 not loaded");
    await expect(restored).toBeFocused();
    expect(world.offsets).toEqual([0, 50, 100]);
  });
});


// The Recent head controls are rebuilt with the screen. The escalated
// checkbox owns a stable focus key so it keeps the keyboard position.
test("the escalated toggle keeps focus after Recent rebuilds", async ({ app }) => {
  await triageMocks(app.page);
  await app.goto("/mail?mode=recent");
  await app.expectRoute("mail");
  const toggle = app.page.getByRole("checkbox", { name: /Escalated only/ });
  await toggle.focus();
  await app.page.keyboard.press("Space");
  await expect(app.page).toHaveURL(/escalated=1/);
  await expect(toggle).toBeChecked();
  await expect(toggle).toBeFocused();
});
