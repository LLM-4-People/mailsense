import { json, fulfillJson, mockApi } from "../fixtures/mock";
import {
  messageView,
  msg,
  searchHit,
  searchView,
  triageResult,
} from "../fixtures/data";
import { expect, test } from "../fixtures/test";

// The omnibox is a suggestion box, not the Search page: every pause in a
// typed prefix must ask for text-only results and must never start the local
// embedding model. Submitting the same text opens the Search page, which
// keeps the semantic default.
test("omnibox typing is text-only and the Search page stays semantic", async ({
  app,
}) => {
  const semantic = searchView();
  semantic.semantic = {
    used: true,
    covered: 2,
    eligible: 2,
    space: "store:e2e",
  };
  semantic.notice = "semantic index covers 2 of 2 messages";
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/search",
      handler: async ({ route }) => {
        await route.fulfill(json(semantic));
      },
    },
  ]);

  const searches: URL[] = [];
  const embedded: URL[] = [];
  app.page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/search") searches.push(url);
    if (/embedding/i.test(url.pathname)) embedded.push(url);
  });

  await app.goto("/hub");
  await app.expectRoute("hub");

  const input = app.page.locator("#global-search-input");
  await input.click();
  await input.pressSequentially("invoice2026", { delay: 260 });
  await expect(app.page.locator(".topbar-suggest").first()).toBeVisible();

  const suggestions = searches.filter((url) => url.searchParams.get("limit") === "6");
  expect(suggestions.length).toBeGreaterThan(1);
  for (const url of suggestions) {
    expect(url.searchParams.get("semantic")).toBe("off");
  }
  expect(embedded).toHaveLength(0);

  // Enter opens the explicit Search page with the full query.
  await input.press("Enter");
  await expect(app.page).toHaveURL(/#\/mail\?mode=search&q=invoice2026/);
  await expect(app.page.locator(".search-notices")).toContainText(
    "Semantic search was used",
  );

  const pageReads = searches.filter((url) => url.searchParams.get("limit") === null);
  expect(pageReads.length).toBeGreaterThan(0);
  for (const url of pageReads) {
    expect(url.searchParams.get("semantic")).not.toBe("off");
  }
  expect(searches.some((url) => url.searchParams.get("semantic") === "off")).toBe(true);
  expect(embedded).toHaveLength(0);
});

// The mailbox list only populates the store filter. A failed read must not
// block the search, keep the query, and say so beside the filter; retry
// recovers the options.
test("a failed mailbox read keeps the search and retries beside the filter", async ({
  app,
}) => {
  let mailboxesFail = true;
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/mailboxes",
      handler: async ({ route }) => {
        if (mailboxesFail) {
          await fulfillJson(
            route,
            { error: { code: "internal", message: "synthetic mailbox failure" } },
            500,
          );
          return;
        }
        await fulfillJson(route, {
          mailboxes: [{ key: "store-e2e", name: "Demo mailbox", selected: true }],
        });
      },
    },
    { method: "GET", path: "/api/search", body: searchView() },
  ]);

  const searches: URL[] = [];
  app.page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/search") searches.push(url);
  });

  await app.goto("/mail?mode=search&q=invoice");
  await app.expectRoute("mail");

  const filters = app.page.locator(".mail-search-filters");
  await expect(filters).toContainText("The mailbox list is unavailable.");
  const store = filters.locator("select").first();
  await expect(store).toHaveValue("");
  await expect(store.locator("option")).toHaveCount(1);
  // The search still ran, without a store filter, and painted its results.
  await expect(app.page.locator(".search-notices")).toContainText("Semantic search is unavailable");
  await expect(app.page.locator(".mail-list")).toContainText("Synthetic invoice");
  const ran = searches.filter((url) => url.searchParams.get("q") === "invoice");
  expect(ran.length).toBeGreaterThan(0);
  for (const url of ran) expect(url.searchParams.has("store")).toBe(false);

  mailboxesFail = false;
  await filters.getByRole("button", { name: "Try again" }).click();

  await expect(app.page.locator(".mail-search-filters .inline-read-error")).toHaveCount(0);
  await expect(store.locator("option")).toHaveCount(2);
  await expect(store.locator("option").nth(1)).toHaveText("Demo mailbox");
});

// A search hit can name a message the server no longer has. Its stored triage
// must never paint a bodyless detail: the message read decides, and a removed
// message paints exactly one unavailable state.
function twoHitSearchView() {
  const view = searchView();
  view.hits = [searchHit(msg.invoice), searchHit(msg.budget)];
  view.total = 2;
  view.text_matches = 2;
  return view;
}

function removedMessageMocks() {
  return [
    { method: "GET" as const, path: "/api/search", body: twoHitSearchView() },
    { method: "GET" as const, path: "/api/triage/101", body: triageResult(msg.invoice) },
    { method: "GET" as const, path: "/api/messages/102", body: messageView(msg.budget) },
    { method: "GET" as const, path: "/api/triage/102", body: triageResult(msg.budget) },
  ];
}

test("a removed search hit shows one unavailable state, not its stored triage", async ({
  app,
}) => {
  await mockApi(app.page, [
    ...removedMessageMocks(),
    {
      method: "GET",
      path: "/api/messages/101",
      handler: async ({ route }) => {
        await fulfillJson(
          route,
          { error: { code: "not_found", message: "synthetic removal" } },
          404,
        );
      },
    },
  ]);

  await app.goto("/mail?mode=search&q=invoice&id=101");
  await app.expectRoute("mail");

  const detail = app.page.locator("#mail-detail");
  await expect(detail.locator(".unavailable")).toHaveCount(1);
  await expect(detail.locator(".unavailable")).toHaveText(
    "This message is no longer available.",
  );
  await expect(
    detail.getByText("This message is no longer available.", { exact: true }),
  ).toHaveCount(1);
  await expect(detail.locator(".mail-subject")).toHaveCount(0);
  await expect(detail.locator(".triage-summary")).toHaveCount(0);
  await expect(detail).not.toContainText("Triage result");
  await expect(detail).not.toContainText("Open in Teach");
  await expect(detail).not.toContainText("Ask about this message");

  // Another hit from the same result page still renders its message normally.
  await app.page.locator('.search-result[data-id="102"]').click();
  await expect(app.page).toHaveURL(/#\/mail\?mode=search&q=invoice&id=102/);
  await expect(detail.locator(".mail-subject")).toHaveText("Synthetic budget");
  await expect(detail.locator(".detail-answers")).toBeVisible();
  await expect(detail.locator(".unavailable")).toHaveCount(0);
});

test("a 410 message read paints the removed state, never the stored triage", async ({
  app,
}) => {
  await mockApi(app.page, [
    ...removedMessageMocks(),
    {
      method: "GET",
      path: "/api/messages/101",
      handler: async ({ route }) => {
        // No error envelope: the client itself spells the transport code.
        await route.fulfill({
          status: 410,
          contentType: "application/json",
          body: "",
        });
      },
    },
  ]);

  await app.goto("/mail?mode=search&q=invoice&id=101");
  await app.expectRoute("mail");

  const detail = app.page.locator("#mail-detail");
  await expect(detail.locator(".unavailable")).toHaveCount(1);
  await expect(detail.locator(".unavailable")).toHaveText(
    "This message is no longer available.",
  );
  await expect(detail.locator(".mail-subject")).toHaveCount(0);
  await expect(detail.locator(".triage-summary")).toHaveCount(0);
});

test("an empty 200 message read is invalid, not a bodyless message", async ({
  app,
}) => {
  await mockApi(app.page, [
    ...removedMessageMocks(),
    {
      method: "GET",
      path: "/api/messages/101",
      handler: async ({ route }) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: "",
        });
      },
    },
  ]);

  await app.goto("/mail?mode=search&q=invoice&id=101");
  await app.expectRoute("mail");

  const detail = app.page.locator("#mail-detail");
  await expect(detail).toContainText(
    "The local API returned an unreadable response.",
  );
  await expect(
    detail.getByRole("button", { name: "Try again" }),
  ).toBeVisible();
  await expect(detail.locator(".mail-subject")).toHaveCount(0);
  await expect(detail.locator(".unavailable")).toHaveCount(0);
  await expect(detail.locator(".triage-summary")).toHaveCount(0);
});

// A capped backend total is a lower bound, not an exact count. The result
// count must not claim precision the search could not establish.
test("a capped search reports its total as a lower bound", async ({ app }) => {
  const view = searchView();
  // Twenty of the two hundred lower-bound results are on this page.
  view.hits = Array.from({ length: 20 }, (_, index) =>
    searchHit(msg.invoice + index),
  );
  view.total = 200;
  view.text_matches = 1;
  view.capped = true;
  view.text_matches_capped = false;
  await mockApi(app.page, [
    { method: "GET", path: "/api/mailboxes", body: { mailboxes: [] } },
    { method: "GET", path: "/api/search", body: view },
    { method: "GET", path: "/api/messages/101", body: messageView(msg.invoice) },
    { method: "GET", path: "/api/triage/101", body: triageResult(msg.invoice) },
  ]);

  await app.goto("/mail?mode=search&q=invoice&id=101");
  await app.expectRoute("mail");
  await expect(app.page.locator(".mail-search-results .mail-row")).toHaveCount(20);

  await expect(app.page.locator("#topbar-meta")).toContainText("At least 200 results");
  await expect(app.page.locator(".mail-search-results .mail-pane-head")).toContainText(
    "At least 200 results",
  );
  await expect(app.page.locator(".search-notices")).toContainText(
    "The search reached its match limit; more results may exist.",
  );
  await expect(app.page.locator("#topbar-meta")).not.toContainText("1 of 200 results");
});

test("an uncapped search keeps its exact result count", async ({ app }) => {
  await mockApi(app.page, [
    { method: "GET", path: "/api/mailboxes", body: { mailboxes: [] } },
    { method: "GET", path: "/api/search", body: searchView() },
    { method: "GET", path: "/api/messages/101", body: messageView(msg.invoice) },
    { method: "GET", path: "/api/triage/101", body: triageResult(msg.invoice) },
  ]);

  await app.goto("/mail?mode=search&q=invoice&id=101");
  await app.expectRoute("mail");
  await expect(app.page.locator("#topbar-meta")).toContainText("1 result");
  await expect(app.page.locator(".search-notices")).not.toContainText(
    "The search reached its match limit",
  );
});

// The sender filter is one chip component shared with All Mail. The search
// screen showed it before this round without any test; a change to the chip
// (its label key, its clear control) could break this screen alone. Clearing
// it must drop only the sender and keep the query and the other filters.
test("an active sender is one removable chip on the search screen", async ({
  app,
}) => {
  await mockApi(app.page, [
    { method: "GET", path: "/api/mailboxes", body: { mailboxes: [] } },
    { method: "GET", path: "/api/search", body: searchView() },
  ]);

  await app.goto("/mail?mode=search&q=invoice&sender=demo%40example.test");
  await app.expectRoute("mail");
  const chip = app.page.locator(".mail-search-filters .sender-chip");
  await expect(chip).toHaveCount(1);
  await expect(chip).toContainText("demo@example.test");

  await chip.getByRole("button", { name: "Clear sender filter" }).click();
  await expect(app.page).not.toHaveURL(/sender=/);
  await expect(app.page).toHaveURL(/q=invoice/);
  await expect(app.page.locator(".mail-search-filters .sender-chip")).toHaveCount(0);
});
