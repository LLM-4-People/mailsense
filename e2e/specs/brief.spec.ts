import { learningView } from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { expect, test } from "../fixtures/test";

// A response without the five section objects is broken, not an empty
// mailbox. The Brief must say the read failed instead of painting five
// "nothing here" sentences, and its retry must be able to recover.
test("a brief payload without its sections shows a failed read", async ({ app }) => {
  let broken = true;
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/brief",
      handler: async ({ route }) => {
        if (broken) {
          await fulfillJson(route, { generated_at: "2026-11-03T10:00:00Z" });
          return;
        }
        await fulfillJson(route, {
          generated_at: "2026-11-03T10:00:00Z",
          must: { count: 0, items: [] },
          waiting: { count: 0, items: [] },
          changed: { count: 0, items: [], since: "" },
          relationships: { count: 0, items: [] },
          topics: { count: 0, items: [], total: 0 },
        });
      },
    },
  ]);

  await app.goto("/brief");
  await app.expectRoute("brief");
  const main = app.page.locator("#main");
  await expect(main).toContainText(
    "The local API returned an unreadable response.",
  );
  await expect(main).not.toContainText("Nothing must be done right now.");
  const retry = main.getByRole("button", { name: "Try again" });
  await expect(retry).toBeVisible();

  broken = false;
  await retry.click();
  await expect(main).toContainText("Nothing must be done right now.");
  await expect(main).not.toContainText(
    "The local API returned an unreadable response.",
  );
});

// The relationship row states the commitments rollup's open-loop partition:
// the organization's total, then the overdue and stored follow-up badges the
// producer reports. A wrong wire field or a missing partition would drop a
// badge without any other symptom, so this pins the rendered copy and the
// omit-when-zero rule (briefRelationshipRow renders no badge for a zero).
test("a relationship row renders its open loops and partitions", async ({
  app,
}) => {
  await mockApi(app.page, [
    { method: "GET", path: "/api/learning", body: learningView({ open: true }) },
    {
      method: "GET",
      path: "/api/brief",
      handler: async ({ route }) => {
        await fulfillJson(route, {
          generated_at: "2026-11-03T10:00:00Z",
          must: { count: 0, items: [] },
          waiting: { count: 0, items: [] },
          changed: { count: 0, items: [], since: "" },
          relationships: {
            count: 2,
            items: [
              {
                id: "organization:c:acme",
                kind: "organization",
                title: "Acme",
                organization: { key: "c:acme", label: "Acme" },
                count: 4,
                overdue: 2,
                stored_due: 3,
              },
              {
                id: "organization:c:beta",
                kind: "organization",
                title: "Beta",
                organization: { key: "c:beta", label: "Beta" },
                count: 1,
              },
            ],
          },
          topics: { count: 0, items: [], total: 0 },
        });
      },
    },
  ]);

  await app.goto("/brief");
  await app.expectRoute("brief");
  const section = app.page.locator("#brief-relationships");
  const rows = section.locator(".brief-rel-row");
  await expect(rows).toHaveCount(2);

  const acme = rows.filter({ hasText: "Acme" });
  await expect(acme).toHaveCount(1);
  await expect(acme).toContainText("4 open loops");
  await expect(acme).toContainText("2 overdue");
  await expect(acme).toContainText("3 stored follow-ups");
  // Secondary: the partitions keep the producer's severity.
  const acmeStats = acme.locator(".brief-rel-stats");
  await expect(acmeStats.locator("span").nth(0)).toHaveText("4 open loops");
  await expect(acmeStats.locator(".badge.danger")).toHaveText("2 overdue");
  await expect(acmeStats.locator(".badge.plain")).toHaveText(
    "3 stored follow-ups",
  );
  await expect(acme).toHaveAttribute("href", "#/companies?key=c%3Aacme");

  // A zero partition renders no badge: the open-loop total is the whole row.
  const beta = rows.filter({ hasText: "Beta" });
  await expect(beta).toHaveCount(1);
  await expect(beta).toContainText("1 open loop");
  await expect(beta).not.toContainText("overdue");
  await expect(beta).not.toContainText("stored");
  await expect(beta.locator(".brief-rel-stats .badge")).toHaveCount(0);
});
