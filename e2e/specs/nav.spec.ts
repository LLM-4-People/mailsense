import { expect, test } from "../fixtures/test";

// The sidebar is the only route menu. These specs exercise the disclosure
// groups, the persisted state, the Answers destination, the narrow-window
// drawer and the brand home link in a real browser.
test.describe("navigation", () => {
  test("groups collapse and the stored state survives a reload", async ({
    app,
  }) => {
    await app.goto("/hub");
    await app.expectRoute("hub");

    const teach = app.page.locator('[data-nav-group="teach"]');
    const teachSummary = teach.locator("summary");
    await expect(teachSummary).toHaveAttribute("aria-expanded", "true");
    await expect(app.page.locator('[data-nav="learning"]')).toBeVisible();

    await teachSummary.click();
    await expect(teachSummary).toHaveAttribute("aria-expanded", "false");
    await expect(app.page.locator('[data-nav="learning"]')).toBeHidden();

    await app.page.reload();
    await expect(app.page.locator("#app")).toBeVisible();
    await app.expectRoute("hub");
    await expect(teachSummary).toHaveAttribute("aria-expanded", "false");
    await expect(app.page.locator('[data-nav="learning"]')).toBeHidden();

    await teachSummary.click();
    await expect(teachSummary).toHaveAttribute("aria-expanded", "true");
    await expect(app.page.locator('[data-nav="learning"]')).toBeVisible();
  });

  test("the active route keeps its group open over a stored closed state", async ({
    app,
  }) => {
    await app.goto("/hub");
    const ask = app.page.locator('[data-nav-group="ask"]');
    const askSummary = ask.locator("summary");

    await askSummary.click();
    await expect(askSummary).toHaveAttribute("aria-expanded", "false");
    await expect(app.page.locator('[data-nav="answers"]')).toBeHidden();

    // The stored state says closed, but Answers owns the route: the group
    // opens for as long as that route is active.
    await app.goto("/answers");
    await app.expectRoute("answers");
    await expect(askSummary).toHaveAttribute("aria-expanded", "true");
    await expect(app.page.locator('[data-nav="answers"]')).toBeVisible();
    await expect(app.page.locator('[data-nav="answers"]')).toHaveAttribute(
      "aria-current",
      "page",
    );

    await app.page.reload();
    await app.expectRoute("answers");
    await expect(askSummary).toHaveAttribute("aria-expanded", "true");
  });

  test("the Answers destination opens the Answers screen", async ({ app }) => {
    await app.goto("/hub");
    await app.page.locator('[data-nav="answers"]').click();
    await app.expectRoute("answers");
    await expect(app.page.locator('[data-nav="answers"]')).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  test("at 600px the toggle opens the drawer and Escape closes it", async ({
    app,
  }) => {
    await app.page.setViewportSize({ width: 600, height: 900 });
    await app.goto("/hub");

    const toggle = app.page.getByRole("button", { name: "Menu" });
    const nav = app.page.locator("#nav-menu");
    await expect(toggle).toBeVisible();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(nav).toBeHidden();

    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(nav).toBeVisible();

    await app.page.keyboard.press("Escape");
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(nav).toBeHidden();
    await expect(toggle).toBeFocused();

    // Choosing a destination closes the drawer as well.
    await toggle.click();
    await expect(nav).toBeVisible();
    await app.page.locator('[data-nav="brief"]').click();
    await app.expectRoute("brief");
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(nav).toBeHidden();
  });

  test("the brand is a link back to Home", async ({ app }) => {
    await app.goto("/brief");
    await app.expectRoute("brief");

    const brand = app.page.locator("a.brand");
    await expect(brand).toHaveAttribute("href", "#/hub");
    await brand.click();
    await app.expectRoute("hub");
  });
});
