import {
  AT,
  agendaView,
  companiesView,
  learningView,
  messageListRow,
  messageView,
  msg,
  priorityView,
  questionsView,
  triageResult,
  statusView
} from "../fixtures/data";
import { fulfillJson, mockApi, type Mock } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { expect, test, type App } from "../fixtures/test";

const newMail = {
  ...messageListRow(901),
  subject: "Synthetic new mail",
  sender_name: "New Sender",
  sender_address: "new@example.test",
  snippet: "A sync just stored this message.",
};

const conversationCounts = {
  awaiting_owner: 0,
  awaiting_other: 0,
  answered_by_owner: 0,
  answered_by_other: 0,
  no_action: 0,
  unknown: 0,
};

function mailPage(rows: ReturnType<typeof messageListRow>[]) {
  return {
    messages: rows,
    total: rows.length,
    counts: {
      inbox: rows.filter((row) => row.role === "inbox").length,
      sent: rows.filter((row) => row.role === "sent").length,
      drafts: 0,
      deleted: 0,
      junk: 0,
      archive: 0,
      other: 0,
    },
    next_before: "",
    has_more: false,
  };
}

// Every completion-event test owns the same gate-open world: only the reads
// whose request counts or rows a test asserts are synthetic.
async function completionMocks(app: App, extra: Mock[] = []): Promise<void> {
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true, preview: false }) },
    { method: "GET", path: "/api/status", body: statusView() },
    { method: "GET", path: "/api/hub", body: { generated_at: AT } },
    { method: "GET", path: "/api/priority", body: priorityView() },
    { method: "GET", path: "/api/agenda", body: agendaView() },
    { method: "GET", path: "/api/companies", body: companiesView([]) },
    { method: "GET", path: "/api/questions", body: questionsView() },
    {
      method: "GET",
      path: "/api/triage/summary",
      body: {
        coverage: { eligible: 2, fresh: 1, incomplete: 0, stale: 0, failed: 0, unjudged: 1 },
        preview: false,
      },
    },
    {
      method: "GET",
      path: "/api/triage",
      body: { total: 1, limit: 50, offset: 0, routed: false, results: { [msg.invoice]: triageResult(msg.invoice) } },
    },
    {
      method: "GET",
      path: "/api/conversations",
      body: {
        generated_at: AT,
        conversations: [],
        total: 0,
        counts: conversationCounts,
        next_before: "",
        has_more: false,
        preview: false,
        coverage: { messages: 0, conversations: 0, unthreaded: 0 },
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
    ...extra,
  ]);
}

function requestsTo(app: App, pathname: string): () => number {
  let count = 0;
  app.page.on("request", (request) => {
    if (new URL(request.url()).pathname === pathname) count += 1;
  });
  return () => count;
}

// Boot prefetch and the live coalescer are both debounced. Counting starts
// only after the mount has settled, so a test's delta belongs to its event.
async function settle(app: App): Promise<void> {
  await app.page.waitForTimeout(1_500);
}

test.describe("completion events", () => {
  test("sync.finished refreshes Mail, Companies and Hub and forgets All Mail", async ({ app }) => {
    await installFakeEventSource(app.page);
    let storedNewMail = false;
    await completionMocks(app, [
      {
        method: "GET",
        path: "/api/messages",
        handler: async ({ route }) => {
          await fulfillJson(
            route,
            mailPage(storedNewMail ? [newMail] : [messageListRow(msg.invoice)]),
          );
        },
      },
    ]);
    const messageReads = requestsTo(app, "/api/messages");
    const companyReads = requestsTo(app, "/api/companies");
    const hubReads = requestsTo(app, "/api/hub");

    await app.goto("/mail?mode=all");
    await app.expectRoute("mail");
    await expect(app.page.locator(".all-mail-row")).toContainText("Synthetic invoice");
    await settle(app);

    const mailBefore = messageReads();
    storedNewMail = true;
    await emitSse(app.page, "sync.finished", { totals: { written: 3 } });
    await expect(app.page.locator(".all-mail-row")).toContainText("Synthetic new mail");
    expect(messageReads()).toBe(mailBefore + 1);

    await app.page.locator('[data-nav="companies"]').click();
    await app.expectRoute("companies");
    await settle(app);
    const companyBefore = companyReads();
    await emitSse(app.page, "sync.finished", { totals: { written: 3 } });
    await expect.poll(() => companyReads(), { timeout: 5_000 }).toBeGreaterThan(companyBefore);

    await app.page.locator('[data-nav="hub"]').click();
    await app.expectRoute("hub");
    await settle(app);
    const hubBefore = hubReads();
    await emitSse(app.page, "sync.finished", { totals: { written: 3 } });
    await expect.poll(() => hubReads(), { timeout: 5_000 }).toBeGreaterThan(hubBefore);

    // The Hub sync dropped the All-Mail model. Back through Companies to the
    // same All-Mail route: the cached screen must not restore its old rows.
    const messagesBeforeReturn = messageReads();
    await app.page.goBack();
    await app.expectRoute("companies");
    await app.page.goBack();
    await app.expectRoute("mail");
    await expect.poll(() => messageReads(), { timeout: 5_000 }).toBeGreaterThan(messagesBeforeReturn);
  });

  test("a sync with no changed mail issues no request", async ({ app }) => {
    await installFakeEventSource(app.page);
    await completionMocks(app, [
      { method: "GET", path: "/api/messages", body: mailPage([messageListRow(msg.invoice)]) },
    ]);
    const messageReads = requestsTo(app, "/api/messages");
    const companyReads = requestsTo(app, "/api/companies");
    const hubReads = requestsTo(app, "/api/hub");
    const priorityReads = requestsTo(app, "/api/priority");

    await app.goto("/mail?mode=all");
    await app.expectRoute("mail");
    await expect(app.page.locator(".all-mail-row")).toContainText("Synthetic invoice");
    await settle(app);
    const before = {
      messages: messageReads(),
      companies: companyReads(),
      hub: hubReads(),
      priority: priorityReads(),
    };

    await emitSse(app.page, "sync.finished", {
      totals: { seen: 5, written: 0, skipped: 5, duplicates: 0 },
      removed: { deleted: 0, moved: 0, held: 0 },
      scope: { judged: 0 },
      details: { completed: 0 },
    });
    await app.page.waitForTimeout(1_200);

    expect(messageReads()).toBe(before.messages);
    expect(companyReads()).toBe(before.companies);
    expect(hubReads()).toBe(before.hub);
    expect(priorityReads()).toBe(before.priority);
  });

  test("triage.finished refreshes Priority, Triage and Conversations but not Mail", async ({ app }) => {
    await installFakeEventSource(app.page);
    await completionMocks(app, [
      { method: "GET", path: "/api/messages", body: mailPage([messageListRow(msg.invoice)]) },
    ]);
    const messageReads = requestsTo(app, "/api/messages");
    const priorityReads = requestsTo(app, "/api/priority");
    const triageReads = requestsTo(app, "/api/triage");
    const conversationReads = requestsTo(app, "/api/conversations");

    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    await expect(app.page.locator(".mail-row").first()).toBeVisible();
    await settle(app);
    let before = priorityReads();
    await emitSse(app.page, "triage.finished", { decided: 2 });
    await expect.poll(() => priorityReads(), { timeout: 5_000 }).toBeGreaterThan(before);

    await app.page.locator(".mail-tabs").getByRole("button", { name: "Recent" }).click();
    await expect(app.page).toHaveURL(/mode=recent/);
    await settle(app);
    before = triageReads();
    await emitSse(app.page, "triage.finished", { decided: 2, partial: 0 });
    await expect.poll(() => triageReads(), { timeout: 5_000 }).toBeGreaterThan(before);

    await app.page.locator(".mail-tabs").getByRole("button", { name: "Conversations" }).click();
    await expect(app.page).toHaveURL(/mode=conversations/);
    await settle(app);
    before = conversationReads();
    await emitSse(app.page, "triage.finished", { decided: 2, partial: 0 });
    await expect.poll(() => conversationReads(), { timeout: 5_000 }).toBeGreaterThan(before);

    // Triage reclassifies stored mail; the All-Mail list membership and rows
    // are unchanged, so its active screen must not re-read.
    await app.page.locator(".mail-tabs").getByRole("button", { name: "All" }).click();
    await expect(app.page).toHaveURL(/mode=all/);
    await settle(app);
    before = messageReads();
    await emitSse(app.page, "triage.finished", { decided: 2, partial: 0 });
    await app.page.waitForTimeout(1_200);
    expect(messageReads()).toBe(before);
  });

  test("a triage run that decided nothing issues no request", async ({ app }) => {
    await installFakeEventSource(app.page);
    await completionMocks(app, [
      { method: "GET", path: "/api/messages", body: mailPage([messageListRow(msg.invoice)]) },
    ]);
    const messageReads = requestsTo(app, "/api/messages");
    const priorityReads = requestsTo(app, "/api/priority");
    const triageReads = requestsTo(app, "/api/triage");
    const conversationReads = requestsTo(app, "/api/conversations");

    await app.goto("/mail?mode=important");
    await app.expectRoute("mail");
    await expect(app.page.locator(".mail-row").first()).toBeVisible();
    await settle(app);
    const before = {
      messages: messageReads(),
      priority: priorityReads(),
      triage: triageReads(),
      conversations: conversationReads(),
    };

    await emitSse(app.page, "triage.finished", { decided: 0, partial: 0, failed: 4, not_attempted: 4 });
    await app.page.waitForTimeout(1_200);

    expect(messageReads()).toBe(before.messages);
    expect(priorityReads()).toBe(before.priority);
    expect(triageReads()).toBe(before.triage);
    expect(conversationReads()).toBe(before.conversations);
  });

  test("config.changed leaves no cached All-Mail model to repaint", async ({ app }) => {
    await installFakeEventSource(app.page);
    await completionMocks(app, [
      { method: "GET", path: "/api/messages", body: mailPage([messageListRow(msg.invoice)]) },
    ]);
    const messageReads = requestsTo(app, "/api/messages");

    await app.goto("/mail?mode=all");
    await app.expectRoute("mail");
    await expect(app.page.locator(".all-mail-row")).toContainText("Synthetic invoice");
    await settle(app);

    await app.page.locator('[data-nav="hub"]').click();
    await app.expectRoute("hub");
    await settle(app);
    const before = messageReads();

    await emitSse(app.page, "config.changed", { keys: ["mail.sync"] });
    await app.page.waitForTimeout(700);
    await app.page.goBack();
    await app.expectRoute("mail");
    await expect.poll(() => messageReads(), { timeout: 5_000 }).toBeGreaterThan(before);
  });

  test("a reconnect refreshes agenda and triage", async ({ app }) => {
    await installFakeEventSource(app.page);
    await completionMocks(app);
    const agendaReads = requestsTo(app, "/api/agenda");

    await app.goto("/hub");
    await app.expectRoute("hub");
    await settle(app);
    let before = agendaReads();
    await emitSse(app.page, "error");
    await emitSse(app.page, "open");
    await expect.poll(() => agendaReads(), { timeout: 5_000 }).toBeGreaterThan(before);

    const triageReads = requestsTo(app, "/api/triage");
    await app.goto("/mail?mode=recent");
    await app.expectRoute("mail");
    await settle(app);
    before = triageReads();
    await emitSse(app.page, "error");
    await emitSse(app.page, "open");
    await expect.poll(() => triageReads(), { timeout: 5_000 }).toBeGreaterThan(before);
  });
});
