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
