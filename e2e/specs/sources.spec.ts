import type { Route } from "@playwright/test";
import { learningView } from "../fixtures/data";
import { fulfillJson, mockApi, type Mock } from "../fixtures/mock";
import { emitSse, installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

function sourcesMocks(learning: (call: number, route: Route) => Promise<void>): Mock[] {
  let calls = 0;
  return [
    {
      method: "GET",
      path: "/api/status",
      body: { sources: [], version: "e2e", time_zone: "UTC", data_dir: "/tmp/e2e" },
    },
    { method: "GET", path: "/api/mailboxes", body: { mailboxes: [] } },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    {
      method: "GET",
      path: "/api/learning",
      handler: async ({ route }) => {
        calls += 1;
        await learning(calls, route);
      },
    },
  ];
}

// A failed jobs read is not "no job". The backfill row says the status is
// unknown and owns the retry; the rest of Sources still paints.
test("a failed jobs read says job status is unavailable and retries", async ({ app }) => {
  await installFakeEventSource(app.page);
  let jobsFail = true;
  let cancelled = false;
  const job = {
    id: "job-e2e",
    kind: "messages.html",
    status: "running",
    name: "Synthetic backfill",
    progress: { done: 2, total: 10 },
  };
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/status",
      body: { sources: [], version: "e2e", time_zone: "UTC", data_dir: "/tmp/e2e" },
    },
    { method: "GET", path: "/api/mailboxes", body: { mailboxes: [] } },
    {
      method: "GET",
      path: "/api/jobs",
      handler: async ({ route }) => {
        if (jobsFail) {
          await fulfillJson(route, { error: { code: "internal", message: "synthetic jobs failure" } }, 500);
          return;
        }
        await fulfillJson(route, { jobs: [job] });
      },
    },
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "POST",
      path: "/api/jobs/job-e2e/cancel",
      handler: async ({ route }) => {
        cancelled = true;
        await fulfillJson(route, { ...job, status: "cancelled", finished_at: "2026-11-03T12:00:00Z" });
      },
    },
  ]);

  await app.goto("/sources");
  await app.expectRoute("sources");
  const backfill = app.page.locator(".backfill");
  await expect(backfill).toContainText("Job status is unavailable.");
  await expect(backfill).not.toContainText("Last backfill");

  jobsFail = false;
  await backfill.getByRole("button", { name: "Try again" }).click();

  await expect(app.page.locator(".backfill .inline-read-error")).toHaveCount(0);
  await expect(app.page.locator(".backfill")).toContainText("Synthetic Backfill");
  await expect(app.page.locator(".backfill")).not.toContainText("Backfill running");
  await expect(app.page.locator(".backfill")).toContainText("2 / 10 messages");

  await backfill.getByRole("button", { name: "Cancel" }).click();
  await expect.poll(() => cancelled).toBe(true);
});

// A failed /api/gate refresh is permission data, not a Sources read: the
// mailbox screen still paints, and only the permission pill is withheld.
test("a failed gate refresh does not hide Sources", async ({ app }) => {
  await installFakeEventSource(app.page);
  const mocks = sourcesMocks(async (_, route) => fulfillJson(route, learningView({ open: true })));
  mocks.push({
    method: "GET",
    path: "/api/gate",
    handler: async ({ route }) => {
      await fulfillJson(route, { error: { code: "internal", message: "synthetic gate failure" } }, 500);
    },
  });
  await mockApi(app.page, mocks);

  await app.goto("/sources");
  await app.expectRoute("sources");
  await expect(app.page.locator(".sources-screen")).toBeVisible();
  await expect(app.page.locator("#topbar-meta")).not.toContainText("Gate");
});

test("a failed Sources Refresh reports one visible error and restores its button", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  await mockApi(
    app.page,
    sourcesMocks(async (call, route) => {
      if (call === 1) {
        await fulfillJson(route, learningView({ open: true }));
        return;
      }
      await fulfillJson(route, { error: { code: "internal", message: "synthetic learning failure" } }, 500);
    }),
  );

  await app.goto("/sources");
  await app.expectRoute("sources");
  await expect(app.page.locator(".sources-screen")).toBeVisible();

  const refresh = app.page.getByRole("button", { name: "Refresh" });
  await expect(refresh).toBeEnabled();
  await refresh.click();

  const toast = app.page.locator("#toasts .toast");
  await expect(toast).toHaveCount(1);
  await expect(toast).toContainText("synthetic learning failure");
  await expect(refresh).toBeEnabled();
});

// A hung read must not keep the Sources control disabled forever. The one
// request timer aborts at 30 s of page time; the action boundary reports the
// timeout and withBusy restores the button.
test("a timed-out Sources Refresh restores its button and reports", async ({ app }) => {
  await installFakeEventSource(app.page);
  await app.page.clock.install({ time: new Date("2026-11-03T12:00:00Z") });
  await mockApi(
    app.page,
    sourcesMocks(async (call, route) => {
      if (call === 1 || call >= 3) {
        await fulfillJson(route, learningView({ open: true }));
        return;
      }
      // The refresh read never answers; the client's own timer must end it.
    }),
  );

  await app.goto("/sources");
  await app.expectRoute("sources");
  await expect(app.page.locator(".sources-screen")).toBeVisible();

  const refresh = app.page.getByRole("button", { name: "Refresh" });
  const pending = app.page.waitForRequest(
    (request) => new URL(request.url()).pathname === "/api/learning",
  );
  await refresh.click();
  await pending;
  await app.page.clock.fastForward(31_000);

  await expect(app.page.locator("#toasts .toast")).toContainText("The request timed out.");
  await expect(refresh).toBeEnabled();
});

// A pick, an unpick and a discovery only invalidate the mailbox and
// mail-derived surfaces. A cached screen they cannot affect is still the
// same DOM node when the owner returns to it.
test("pick, unpick and discover keep an unrelated cached route", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  let selected = false;
  const mailbox = () => ({
    key: "store-e2e",
    name: "Demo mailbox",
    source_id: "src-e2e",
    kind: "outlook",
    selected,
    discovered: true,
    default: true,
    folders: 2,
    items: 10,
  });
  const mailboxList = () => ({ mailboxes: [mailbox()] });

  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/status",
      body: {
        sources: [{ id: "src-e2e", kind: "outlook" }],
        version: "e2e",
        time_zone: "UTC",
        data_dir: "/tmp/e2e",
      },
    },
    { method: "GET", path: "/api/mailboxes", handler: async ({ route }) => fulfillJson(route, mailboxList()) },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-11-03T10:00:00Z" } },
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "POST",
      path: "/api/mailboxes/pick",
      handler: async ({ route }) => {
        selected = true;
        await fulfillJson(route, mailboxList());
      },
    },
    {
      method: "POST",
      path: "/api/mailboxes/unpick",
      handler: async ({ route }) => {
        selected = false;
        await fulfillJson(route, mailboxList());
      },
    },
    {
      method: "POST",
      path: "/api/mailboxes/discover",
      handler: async ({ route }) => fulfillJson(route, mailboxList()),
    },
    {
      method: "GET",
      path: "/api/logs",
      body: {
        entries: [],
        health: { file_errors: 0, file_failing: false, last_file_error: "", bus_dropped: 0 },
        oldest_seq: 0,
        last_seq: 0,
        truncated: false,
      },
    },
  ]);

  await app.goto("/logs");
  await app.expectRoute("logs");
  await expect(app.page.locator(".logs-screen")).toBeVisible();
  await app.page.locator(".logs-screen").evaluate((node) => {
    node.setAttribute("data-e2e-stable", "logs");
  });

  await app.page.locator('[data-nav="sources"]').click();
  await app.expectRoute("sources");
  const pick = app.page.locator(".sources-mailbox-table input[type=checkbox]");
  await expect(pick).not.toBeChecked();
  await pick.click();
  await expect(
    app.page.locator("#toasts .toast").filter({ hasText: "Mailbox picked" }),
  ).toHaveCount(1);
  await expect(app.page.locator(".sources-mailbox-table input[type=checkbox]")).toBeChecked();

  await app.page.locator(".sources-mailbox-table input[type=checkbox]").click();
  const modal = app.page.locator("#modal");
  await expect(modal).toBeVisible();
  await modal.getByRole("button", { name: "Unpick" }).click();
  await expect(
    app.page.locator("#toasts .toast").filter({ hasText: "Mailbox unpicked" }),
  ).toHaveCount(1);
  await expect(app.page.locator(".sources-mailbox-table input[type=checkbox]")).not.toBeChecked();

  const discover = app.page.locator(".sources-screen").getByRole("button", { name: "Discover" });
  await discover.click();
  await expect(
    app.page.locator("#toasts .toast").filter({ hasText: "Discovery finished" }),
  ).toHaveCount(1);

  await app.page.goBack();
  await app.expectRoute("logs");
  await expect(app.page.locator('.logs-screen[data-e2e-stable="logs"]')).toHaveCount(1);
  await expect(app.page.locator(".logs-screen")).toBeVisible();
});

type HeldFolder = {
  source_id: string;
  folder_id: number;
  store_id: string;
  path: string;
  held: number;
  stored: number;
  since: string;
  applies_at?: string;
};

test("held removals render, confirm by folder id, and leave when the list empties", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  let held: HeldFolder[] = [
    {
      source_id: "outlook",
      folder_id: 7,
      store_id: "store-e2e",
      path: "Inbox",
      held: 3,
      stored: 100,
      since: "2026-11-01T00:00:00Z",
      applies_at: "2026-11-06T00:00:00Z",
    },
    {
      source_id: "outlook",
      folder_id: 9,
      store_id: "store-e2e",
      path: "Archive",
      held: 2,
      stored: 40,
      since: "2026-11-02T09:00:00Z",
    },
  ];
  const posted: string[] = [];
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/status",
      body: {
        sources: [{ id: "outlook", kind: "outlook" }],
        version: "e2e",
        time_zone: "UTC",
        data_dir: "/tmp/e2e",
      },
    },
    { method: "GET", path: "/api/mailboxes", body: { mailboxes: [] } },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-11-03T10:00:00Z" } },
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/held-removals",
      handler: async ({ route }) => fulfillJson(route, { folders: held }),
    },
    {
      method: "POST",
      path: /^\/api\/held-removals\/\d+\/confirm$/,
      handler: async ({ route, url }) => {
        const folderID = url.pathname.split("/")[3];
        posted.push(folderID);
        const folder = held.find((item) => String(item.folder_id) === folderID);
        if (!folder) {
          await fulfillJson(route, { error: { code: "not_found", message: "synthetic gone" } }, 404);
          return;
        }
        held = held.filter((item) => String(item.folder_id) !== folderID);
        await fulfillJson(route, { removal: { deleted: folder.held, moved: 0, held: 0 } });
      },
    },
  ]);

  await app.goto("/sources");
  await app.expectRoute("sources");

  const rows = app.page.locator(".held-removal");
  await expect(rows).toHaveCount(2);
  const inbox = rows.filter({ hasText: "Inbox" });
  const archive = rows.filter({ hasText: "Archive" });
  await expect(inbox).toContainText("3 messages disappeared from a complete sync of Inbox.");
  await expect(inbox).toContainText("100 messages stored");
  await expect(inbox).toContainText("since");
  await expect(archive).toContainText("2 messages disappeared from a complete sync of Archive.");
  // A folder with applies_at states the deadline a later sync applies it; a
  // folder without it states that only confirmation applies it. The old copy
  // promised the removal stayed in place.
  await expect(
    inbox.locator(".held-removal-applies"),
  ).toHaveText(
    "The removal is applied automatically on Nov 06, 2026, 12:00 AM unless you confirm it sooner.",
  );
  await expect(
    archive.locator(".held-removal-applies"),
  ).toHaveText("The removal is applied only when you confirm it.");
  await expect(app.page.locator(".held-removals")).not.toContainText(
    "stays in place",
  );

  const modal = app.page.locator("#modal");
  const inboxConfirm = inbox.getByRole("button", { name: "Confirm removal" });
  await inboxConfirm.click();
  await expect(modal).toBeVisible();
  await expect(modal).toContainText("Remove 3 messages from Inbox?");
  await modal.getByRole("button", { name: "Cancel" }).click();
  await expect(modal).toBeHidden();
  // The dialog returns the keyboard to the row that opened it.
  await expect(inboxConfirm).toBeFocused();

  await inboxConfirm.click();
  await expect(modal).toContainText("Remove 3 messages from Inbox?");
  await modal.getByRole("button", { name: "Confirm removal" }).click();

  await expect.poll(() => posted).toEqual(["7"]);
  await expect(
    app.page.locator("#toasts .toast").filter({ hasText: "Removal confirmed" }),
  ).toContainText("3 removed, 0 moved");
  await expect(app.page.locator(".held-removal")).toHaveCount(1);
  await expect(app.page.locator(".held-removal").filter({ hasText: "Inbox" })).toHaveCount(0);
  // The card was replaced: focus moves to the next held row, not the body.
  const archiveConfirm = app.page
    .locator(".held-removal")
    .filter({ hasText: "Archive" })
    .getByRole("button", { name: "Confirm removal" });
  await expect(archiveConfirm).toBeFocused();

  await archiveConfirm.click();
  await expect(modal).toContainText("Remove 2 messages from Archive?");
  await modal.getByRole("button", { name: "Confirm removal" }).click();

  await expect.poll(() => posted).toEqual(["7", "9"]);
  await expect(app.page.locator(".held-removal")).toHaveCount(0);
  await expect(app.page.locator(".held-removals")).toHaveCount(0);
  await expect(app.page.locator("#main")).toBeFocused();
});

test("an empty held-removals list renders no confirmation card", async ({ app }) => {
  await installFakeEventSource(app.page);
  await mockApi(app.page, [
    ...sourcesMocks(async (_, route) => fulfillJson(route, learningView({ open: true }))),
    { method: "GET", path: "/api/held-removals", body: { folders: [] } },
  ]);

  await app.goto("/sources");
  await app.expectRoute("sources");
  await expect(app.page.locator(".sources-screen")).toBeVisible();
  await expect(app.page.locator(".held-removal")).toHaveCount(0);
  await expect(
    app.page.getByRole("heading", { name: "Messages waiting for confirmation" }),
  ).toHaveCount(0);
});

test("a finished sync refreshes the held-removal list", async ({ app }) => {
  await installFakeEventSource(app.page);
  let held: HeldFolder[] = [];
  await mockApi(app.page, [
    ...sourcesMocks(async (_, route) => fulfillJson(route, learningView({ open: true }))),
    {
      method: "GET",
      path: "/api/held-removals",
      handler: async ({ route }) => fulfillJson(route, { folders: held }),
    },
  ]);

  await app.goto("/sources");
  await app.expectRoute("sources");
  await expect(app.page.locator(".held-removal")).toHaveCount(0);

  held = [{
    source_id: "outlook",
    folder_id: 11,
    store_id: "store-e2e",
    path: "Inbox",
    held: 1,
    stored: 50,
    since: "2026-11-03T10:00:00Z",
  }];
  await emitSse(app.page, "sync.finished", { totals: { written: 0 } });

  await expect(app.page.locator(".held-removal")).toHaveCount(1);
  await expect(app.page.locator(".held-removal")).toContainText(
    "1 message disappeared from a complete sync of Inbox.",
  );
});
