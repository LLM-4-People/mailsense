import { agendaView, learningView, priorityView } from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

// Home draws its shell and the agenda column as soon as their own reads
// settle. A cold priority read is the slow one; it fills the Important
// column later without replacing the mounted screen or the agenda node.
test("a slow priority read fills in place after the agenda paints", async ({
  app,
}) => {
  let releasePriority!: () => void;
  const held = new Promise<void>((resolve) => {
    releasePriority = resolve;
  });
  let prioritySeen!: () => void;
  const seen = new Promise<void>((resolve) => {
    prioritySeen = resolve;
  });

  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-11-03T10:00:00Z" } },
    { method: "GET", path: "/api/agenda", body: agendaView() },
    {
      method: "GET",
      path: "/api/priority",
      handler: async ({ route }) => {
        prioritySeen();
        await held;
        await fulfillJson(route, priorityView());
      },
    },
    { method: "GET", path: "/api/ask/history", body: { items: [] } },
  ]);

  await app.goto("/hub");
  await app.expectRoute("hub");
  await seen;

  const agenda = app.page.locator(".home-section-agenda");
  const important = app.page.locator(".home-section-important");
  await expect(agenda.locator(".agenda-row")).toHaveCount(3);
  await expect(important.locator(".mail-row")).toHaveCount(0);
  await expect(important.locator(".agenda-skeleton")).toBeVisible();

  // Mark the mounted screen and the agenda section, put the agenda body at a
  // real offset, and focus one of its rows: the later priority fill must not
  // rebuild any of them.
  await app.page.evaluate(() => {
    document.querySelector(".home-screen")?.setAttribute("data-e2e-stable", "home");
    const body = document.querySelector(
      '[data-scroll-key="home-agenda"]',
    ) as HTMLElement | null;
    if (!body) return;
    const filler = document.createElement("div");
    filler.style.height = "1200px";
    body.append(filler);
    body.scrollTop = 90;
  });
  const firstAgendaLink = agenda.locator("a.agenda-row-link").first();
  await firstAgendaLink.focus();
  const scrollBefore = await app.page.evaluate(() => {
    const body = document.querySelector(
      '[data-scroll-key="home-agenda"]',
    ) as HTMLElement;
    return body.scrollTop;
  });

  releasePriority();

  await expect(important.locator(".mail-row")).toHaveCount(2);
  // The section's own action must land on the mode it counts: a bare #/mail
  // would inherit whatever mode the Mail screen chose last.
  await expect(important.locator(".section-actions a")).toHaveAttribute(
    "href",
    "#/mail?mode=important",
  );
  await expect(app.page.locator('.home-screen[data-e2e-stable="home"]')).toHaveCount(1);
  await expect(agenda.locator(".agenda-row")).toHaveCount(3);
  await expect(firstAgendaLink).toBeFocused();
  const scrollAfter = await app.page.evaluate(() => {
    const body = document.querySelector(
      '[data-scroll-key="home-agenda"]',
    ) as HTMLElement;
    return body.scrollTop;
  });
  expect(scrollAfter).toBe(scrollBefore);
});

// Leaving Home before a cold read lands must not freeze the cached skeleton.
// The read that started with the render still fills that node, so Back shows
// the content without a rebuild.
test("a cached Home fills after a cold read lands while the owner is away", async ({
  app,
}) => {
  await installFakeEventSource(app.page);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let prioritySeen!: () => void;
  const sawPriority = new Promise<void>((resolve) => {
    prioritySeen = resolve;
  });
  let agendaSeen!: () => void;
  const sawAgenda = new Promise<void>((resolve) => {
    agendaSeen = resolve;
  });

  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-11-03T10:00:00Z" } },
    {
      method: "GET",
      path: "/api/priority",
      handler: async ({ route }) => {
        prioritySeen();
        await held;
        await fulfillJson(route, priorityView());
      },
    },
    {
      method: "GET",
      path: "/api/agenda",
      handler: async ({ route }) => {
        agendaSeen();
        await held;
        await fulfillJson(route, agendaView());
      },
    },
    { method: "GET", path: "/api/ask/history", body: { items: [] } },
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

  await app.goto("/hub");
  await app.expectRoute("hub");
  await sawPriority;
  await sawAgenda;
  await expect(app.page.locator(".home-section-important .agenda-skeleton")).toBeVisible();
  await app.page.locator(".home-screen").evaluate((node) => {
    node.setAttribute("data-e2e-stable", "home");
  });

  await app.page.locator('[data-nav="logs"]').click();
  await app.expectRoute("logs");
  release();

  await app.page.goBack();
  await app.expectRoute("hub");
  await expect(app.page.locator('.home-screen[data-e2e-stable="home"]')).toHaveCount(1);
  await expect(app.page.locator(".home-section-important .mail-row")).toHaveCount(2);
  await expect(app.page.locator(".home-section-agenda .agenda-row")).toHaveCount(3);
});

// The agenda counts describe the whole result even when the list is a cut
// page. The notice is the only signal that the rows and the summary differ.
test("a truncated agenda says the list is cut", async ({ app }) => {
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-11-03T10:00:00Z" } },
    { method: "GET", path: "/api/priority", body: priorityView() },
    { method: "GET", path: "/api/agenda", body: agendaView({ truncated: true }) },
    { method: "GET", path: "/api/ask/history", body: { items: [] } },
  ]);

  await app.goto("/hub");
  await app.expectRoute("hub");
  const agenda = app.page.locator(".home-section-agenda");
  await expect(agenda.locator(".agenda-row")).toHaveCount(3);
  await expect(agenda.locator(".agenda-truncated")).toHaveText(
    "The list is cut short; the counts describe the whole result.",
  );
});

// The horizon switch rebuilds the agenda section. A keyboard activation must
// leave focus on the chip that now carries the pressed state, not on <body>.
test("a keyboard agenda horizon switch keeps focus on the pressed chip", async ({
  app,
}) => {
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-11-03T10:00:00Z" } },
    { method: "GET", path: "/api/priority", body: priorityView() },
    {
      method: "GET",
      path: "/api/agenda",
      handler: async ({ route, url }) => {
        const hours = Number(url.searchParams.get("hours") || 0);
        await fulfillJson(route, agendaView(hours ? { horizon_hours: hours } : {}));
      },
    },
    { method: "GET", path: "/api/ask/history", body: { items: [] } },
  ]);

  await app.goto("/hub");
  await app.expectRoute("hub");
  const group = app.page.getByRole("group", { name: "Agenda horizon" });
  const chip = group.getByRole("button", { name: "8 h" });
  await expect(group.getByRole("button", { name: "3 h" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await chip.focus();
  await app.page.keyboard.press("Enter");

  await expect(chip).toHaveAttribute("aria-pressed", "true");
  await expect(chip).toBeFocused();
  await expect(app.page.locator("#home-agenda-title")).toHaveText("Next 8 h");
});

test("an untruncated agenda has no cut notice", async ({ app }) => {
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    { method: "GET", path: "/api/hub", body: { generated_at: "2026-11-03T10:00:00Z" } },
    { method: "GET", path: "/api/priority", body: priorityView() },
    { method: "GET", path: "/api/agenda", body: agendaView() },
    { method: "GET", path: "/api/ask/history", body: { items: [] } },
  ]);

  await app.goto("/hub");
  await app.expectRoute("hub");
  await expect(app.page.locator(".home-section-agenda .agenda-row")).toHaveCount(3);
  await expect(app.page.locator(".agenda-truncated")).toHaveCount(0);
});
