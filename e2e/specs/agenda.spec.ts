import { agendaView, learningView } from "../fixtures/data";
import { mockApi } from "../fixtures/mock";
import { expect, test } from "../fixtures/test";

test.describe("Agenda", () => {
  test("the Home agenda renders the projected rows and counts", async ({
    app,
  }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/learning", body: learningView() },
      { method: "GET", path: "/api/priority", body: { items: [], total: 0 } },
      { method: "GET", path: "/api/agenda", body: agendaView() },
      {
        method: "GET",
        path: "/api/brief",
        body: {
          must: { count: 0, items: [] },
          waiting: { count: 0, items: [] },
          changed: { count: 0, items: [], since: "" },
          relationships: { count: 0, items: [] },
          topics: { count: 0, items: [], total: 0 },
        },
      },
    ]);
    await app.goto("/hub");
    await app.expectRoute("hub");

    const section = app.page.locator(".home-section-agenda");
    const rows = section.locator(".agenda-row");
    await expect(rows).toHaveCount(3);
    await expect(rows.nth(0)).toContainText("Synthetic review");
    await expect(rows.nth(0)).toContainText("Project kickoff");
    await expect(rows.nth(0)).toContainText("Room A");
    await expect(rows.nth(1)).toContainText("Synthetic renewal");
    await expect(rows.nth(1).locator(".agenda-meta")).toContainText(
      "Before Synthetic review",
    );
    await expect(rows.nth(1)).toHaveAttribute("href", /id=102/);
    await expect(rows.nth(2)).toContainText("Synthetic invoice");
    await expect(rows.nth(2)).toHaveAttribute("href", /id=101/);
    await expect(section).toContainText("1 events · 1 replies owed · 1 due");
    await expect(section).toContainText("2 more waiting");
  });
});
