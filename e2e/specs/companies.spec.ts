import { mockCompanies } from "../fixtures/companies";
import { companiesView, companyProfile, companyRow } from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { expect, hoverNav, test } from "../fixtures/test";

test.describe("Companies", () => {
  test("selecting a company loads its profile and people", async ({ app }) => {
    const cspViolations: string[] = [];
    app.page.on("console", (message) => {
      if (/content security policy/i.test(message.text())) {
        cspViolations.push(message.text());
      }
    });
    await mockCompanies(app.page);
    await app.goto("/companies");
    await app.expectRoute("companies");

    await expect(app.page.locator(".companies-row")).toHaveCount(2);
    await expect(app.page.locator("#companies-profile-title")).toHaveText(
      "Acme",
    );
    await expect(app.page.locator(".companies-people-table")).toContainText(
      "Ada Lovelace",
    );
    await expect(app.page.locator(".companies-people-table")).toContainText(
      "ada@acme.test",
    );
    await expect(app.page.locator(".companies-people-table")).toContainText(
      "+1 555 010 0100",
    );

    // The volume chart is drawn with element-scoped heights. Under
    // style-src 'self' an inline style attribute is refused, so this reads 0
    // and the chart silently disappears.
    const barHeights = await app.page
      .locator(".companies-bar")
      .evaluateAll((elements) =>
        elements.map((element) => element.getBoundingClientRect().height),
      );
    expect(barHeights.filter((height) => height > 0)).toHaveLength(3);
    expect(cspViolations).toEqual([]);

    await app.page.locator(".companies-row").filter({ hasText: "Beta" }).click();
    await expect(app.page).toHaveURL(/key=c%3Abeta/);
    await expect(app.page.locator("#companies-profile-title")).toHaveText(
      "Beta",
    );
    await expect(app.page.locator(".companies-people-table")).toContainText(
      "Grace Hopper",
    );
  });

  test("a deep link outside the ranked list keeps its profile across Back", async ({
    app,
  }) => {
    await mockCompanies(app.page);
    await app.goto("/companies?key=c%3Aexternal");
    await app.expectRoute("companies");
    await expect(app.page.locator("#companies-profile-title")).toHaveText(
      "External Co",
    );
    await expect(app.page.locator(".companies-row.selected")).toHaveCount(0);

    await app.page.locator(".companies-row").filter({ hasText: "Acme" }).click();
    await expect(app.page.locator("#companies-profile-title")).toHaveText(
      "Acme",
    );
    await expect(app.page.locator(".companies-row.selected")).toHaveCount(1);

    await app.page.goBack();
    await expect(app.page).toHaveURL(/key=c%3Aexternal/);
    await expect(app.page.locator("#companies-profile-title")).toHaveText(
      "External Co",
    );
    await expect(app.page.locator(".companies-row.selected")).toHaveCount(0);
    // The cached screen must still restore the list count in the topbar.
    await expect(app.page.locator("#topbar-meta")).toContainText("2 companies");
  });

  test("the list search filters rows without refetching", async ({ app }) => {
    await mockCompanies(app.page);
    await app.goto("/companies");
    const search = app.page.locator("#global-search-input");
    await search.fill("Beta");
    await expect(search).toHaveValue("Beta");
    await expect(
      app.page.locator(".companies-row").filter({ hasText: "Acme" }),
    ).toBeHidden();
    await expect(
      app.page.locator(".companies-row").filter({ hasText: "Beta" }),
    ).toBeVisible();
    await expect(app.page.locator(".companies-list-count")).toContainText(
      "1 of 2",
    );
    // The profile still shows Acme; say so instead of leaving the panes
    // silently disagreeing.
    await expect(app.page.locator(".companies-search-note")).toContainText(
      "Acme is selected but outside this search.",
    );
  });

  test("the profile states the evidence, the next step and both ledgers", async ({
    app,
  }) => {
    await mockCompanies(app.page);
    await app.goto("/companies");
    await app.expectRoute("companies");

    await expect(
      app.page.locator("#companies-intelligence-title"),
    ).toBeVisible();
    await expect(app.page.locator(".companies-brief-list")).toContainText(
      "Last 90 days",
    );
    await expect(app.page.locator(".companies-next")).toContainText(
      "Next step",
    );
    await expect(app.page.locator(".companies-next")).toContainText(
      "Reply to “Synthetic renewal”",
    );
    await expect(app.page.locator(".companies-ledger")).toContainText(
      "You owe",
    );
    await expect(app.page.locator(".companies-ledger")).toContainText(
      "Waiting on them",
    );
    await expect(app.page.locator(".companies-loop-row")).toContainText(
      "Their message is last",
    );
    await expect(app.page.locator(".companies-loop-row")).toContainText(
      "3 messages",
    );
    await expect(app.page.locator(".companies-people-table")).toContainText(
      "1 sent · 2 received",
    );
    await expect(app.page.locator(".companies-recent-side")).toContainText(
      "Attachment",
    );
    await expect(app.page.locator(".companies-event-meta")).toContainText(
      "Organized by Ada Lovelace",
    );
    await expect(app.page.locator(".companies-event-meta")).toContainText(
      "3 attendees",
    );
    await expect(app.page.locator(".companies-event-online")).toHaveAttribute(
      "href",
      "https://meet.example.test/review",
    );
    const ask = app.page.getByRole("button", {
      name: "Ask about this company",
    });
    await expect(ask).toBeVisible();
    await ask.click();
    await expect(app.page).toHaveURL(/#\/ask/);
    await expect(app.page.locator("#ask-input")).toHaveValue(
      "Tell me about my relationship with Acme",
    );
  });

  test("selecting the failed company again retries its profile", async ({
    app,
  }) => {
    let profileReads = 0;
    await mockApi(app.page, [
      { method: "GET", path: "/api/companies", body: companiesView() },
      {
        method: "GET",
        path: "/api/companies/profiles",
        body: { generated_at: "2026-10-25T10:00:00Z", profiles: {} },
      },
      {
        method: "GET",
        path: /^\/api\/companies\/.+$/,
        handler: async ({ route }) => {
          profileReads += 1;
          if (profileReads === 1) {
            await fulfillJson(route, { error: { code: "internal" } }, 500);
            return;
          }
          await fulfillJson(
            route,
            companyProfile("c:acme", "Acme", "acme.test"),
          );
        },
      },
    ]);
    await app.goto("/companies");
    await app.expectRoute("companies");
    await expect(app.page.locator(".companies-profile-state")).toContainText(
      "Profile unavailable",
    );
    await app.page.locator(".companies-row").filter({ hasText: "Acme" }).click();
    await expect(app.page.locator("#companies-profile-title")).toHaveText(
      "Acme",
    );
    expect(profileReads).toBeGreaterThanOrEqual(2);
  });

  test("a deep link survives an empty ranked list", async ({ app }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/companies", body: companiesView([]) },
      {
        method: "GET",
        path: /^\/api\/companies\/.+$/,
        handler: async ({ route }) => {
          await fulfillJson(
            route,
            companyProfile("c:external", "External Co", "external.test"),
          );
        },
      },
    ]);
    await app.goto("/companies?key=c%3Aexternal");
    await app.expectRoute("companies");
    await expect(app.page.locator("#companies-profile-title")).toHaveText(
      "External Co",
    );
    await expect(app.page.locator(".companies-list-empty")).toContainText(
      "No companies yet",
    );
  });

  test("a valid deep-link profile survives a list failure", async ({ app }) => {
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/companies",
        handler: async ({ route }) => {
          await fulfillJson(route, { error: { code: "internal" } }, 500);
        },
      },
      {
        method: "GET",
        path: /^\/api\/companies\/.+$/,
        handler: async ({ route }) => {
          await fulfillJson(
            route,
            companyProfile("c:external", "External Co", "external.test"),
          );
        },
      },
    ]);
    await app.goto("/companies?key=c%3Aexternal");
    await app.expectRoute("companies");
    await expect(app.page.locator("#companies-profile-title")).toHaveText(
      "External Co",
    );
    await expect(app.page.locator(".companies-list-empty")).toContainText(
      "Companies unavailable",
    );
  });
});


test("a company below the fold swaps only the profile and keeps both scroll positions", async ({
  app,
}) => {
  const list = Array.from({ length: 20 }, (_, index) =>
    companyRow(
      "c:company" + index,
      "Company " + String(index + 1).padStart(2, "0"),
      "company" + index + ".test",
    ),
  );
  const singles: string[] = [];
  let batches = 0;
  await mockApi(app.page, [
    { method: "GET", path: "/api/companies", body: companiesView(list) },
    {
      method: "GET",
      path: "/api/companies/profiles",
      handler: async ({ route, url }) => {
        batches += 1;
        const profiles: Record<string, unknown> = {};
        for (const key of url.searchParams.getAll("key")) {
          // Company 14 must stay cold for the click below: after the list
          // scrolls, the observer may prefetch every visible row, and a
          // prefetched profile swaps synchronously with no updating bar.
          if (key === "c:company13") continue;
          const row = list.find((item) => item.key === key);
          if (row) profiles[key] = companyProfile(key, row.label, row.domains[0]);
        }
        await fulfillJson(route, {
          generated_at: "2026-10-29T12:00:00Z",
          profiles,
        });
      },
    },
    {
      method: "GET",
      path: /^\/api\/companies\/.+$/,
      handler: async ({ route, url }) => {
        const key = decodeURIComponent(url.pathname.split("/").pop() || "");
        singles.push(key);
        await new Promise((resolve) => setTimeout(resolve, 400));
        const row = list.find((item) => item.key === key);
        await fulfillJson(
          route,
          row
            ? companyProfile(key, row.label, row.domains[0])
            : { code: "unknown_company" },
          row ? 200 : 404,
        );
      },
    },
  ]);

  await app.page.setViewportSize({ width: 1280, height: 800 });
  await app.goto("/companies");
  await expect(app.page.locator(".companies-row")).toHaveCount(20);
  await expect(app.page.locator("#companies-profile-title")).toHaveText(
    "Company 01",
  );
  await expect.poll(() => batches).toBeGreaterThan(0);

  // A visible row that the observer prefetched is a synchronous swap: no
  // single-profile request and no updating state. The batch is applied a
  // microtask after its response, so poll the effect instead of sleeping.
  const clickCompany05 = () =>
    app.page.evaluate(() => {
      const row = [...document.querySelectorAll(".companies-row")].find((node) =>
        (node.textContent || "").includes("Company 05"),
      ) as HTMLElement | undefined;
      row?.click();
      return {
        title:
          document.querySelector("#companies-profile-title")?.textContent || "",
        updating: document.querySelectorAll(".companies-profile-updating").length,
      };
    });
  await expect
    .poll(async () => {
      const now = await clickCompany05();
      return now.title === "Company 05" && now.updating === 0;
    }, { message: "the prefetched profile never became a synchronous swap" })
    .toBe(true);
  const prefetchedSingles = singles.length;
  expect(await clickCompany05()).toEqual({ title: "Company 05", updating: 0 });
  expect(singles.length).toBe(prefetchedSingles);

  await app.page.evaluate(() => {
    const listBody = document.querySelector(".companies-list-body") as HTMLElement;
    listBody.scrollTop = 600;
    (window as any).__companiesList = listBody;
    const profile = document.querySelector(".companies-profile") as HTMLElement;
    profile.scrollTop = 200;
  });
  const before = await app.page.evaluate(() => {
    const listBody = document.querySelector(".companies-list-body") as HTMLElement;
    const profile = document.querySelector(".companies-profile") as HTMLElement;
    const main = document.querySelector("#main") as HTMLElement;
    return { list: listBody.scrollTop, profile: profile.scrollTop, main: main.scrollTop };
  });
  expect(before.list).toBeGreaterThan(0);
  expect(before.profile).toBeGreaterThan(0);
  expect(before.main).toBe(0);

  // Click in the page, not through Playwright's actionability scroll, so the
  // assertion is about the app and not the test runner.
  const immediate = await app.page.evaluate(() => {
    const row = [...document.querySelectorAll(".companies-row")].find((node) =>
      (node.textContent || "").includes("Company 14"),
    ) as HTMLElement | undefined;
    row?.click();
    const listBody = document.querySelector(".companies-list-body") as HTMLElement;
    const profile = document.querySelector(".companies-profile") as HTMLElement;
    const main = document.querySelector("#main") as HTMLElement;
    return {
      sameList: listBody === (window as any).__companiesList,
      list: listBody.scrollTop,
      profile: profile.scrollTop,
      main: main.scrollTop,
      screenSkeleton: document.querySelectorAll(".companies-skeleton-list").length,
      updating: document.querySelectorAll(".companies-profile-updating").length,
    };
  });
  expect(immediate.sameList).toBe(true);
  expect(immediate.list).toBe(before.list);
  expect(immediate.profile).toBe(before.profile);
  expect(immediate.main).toBe(0);
  expect(immediate.screenSkeleton).toBe(0);
  expect(immediate.updating).toBe(1);

  await expect(app.page.locator("#companies-profile-title")).toHaveText(
    "Company 14",
  );
  const after = await app.page.evaluate(() => {
    const listBody = document.querySelector(".companies-list-body") as HTMLElement;
    const profile = document.querySelector(".companies-profile") as HTMLElement;
    const main = document.querySelector("#main") as HTMLElement;
    return {
      sameList: listBody === (window as any).__companiesList,
      list: listBody.scrollTop,
      client: listBody.clientHeight,
      scroll: listBody.scrollHeight,
      profile: profile.scrollTop,
      main: main.scrollTop,
    };
  });
  expect(after.sameList).toBe(true);
  // The selection never rebuilds the list, so its position survives — unless
  // the screen gained room meanwhile (the preview banner leaves once the
  // learning gate answers) and the browser clamps it to the new maximum.
  expect(after.list).toBe(Math.min(before.list, after.scroll - after.client));
  expect(after.profile).toBe(before.profile);
  expect(after.main).toBe(0);
});

test("hovering a nav destination keeps the Companies query, selection and profile", async ({
  app,
}) => {
  const list = [
    companyRow("c:acme", "Acme", "acme.test"),
    companyRow("c:beta", "Beta", "beta.test"),
  ];
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/companies",
      handler: async ({ route, url }) => {
        const query = String(url.searchParams.get("q") || "").trim().toLowerCase();
        const companies = query
          ? list.filter((company) => company.label.toLowerCase().includes(query))
          : list;
        await fulfillJson(route, companiesView(companies));
      },
    },
    {
      method: "GET",
      path: "/api/companies/profiles",
      handler: async ({ route, url }) => {
        const profiles: Record<string, unknown> = {};
        for (const key of url.searchParams.getAll("key")) {
          const company = list.find((item) => item.key === key);
          if (company) profiles[key] = companyProfile(key, company.label, company.domains[0]);
        }
        await fulfillJson(route, { generated_at: "2026-10-29T12:00:00Z", profiles });
      },
    },
    {
      method: "GET",
      path: /^\/api\/companies\/(?!profiles$).+$/,
      handler: async ({ route, url }) => {
        const key = decodeURIComponent(url.pathname.split("/").pop() || "");
        const company = list.find((item) => item.key === key);
        await fulfillJson(
          route,
          company
            ? companyProfile(key, company.label, company.domains[0])
            : { code: "unknown_company" },
          company ? 200 : 404,
        );
      },
    },
  ]);

  // Start on the filtered route so the bare nav destination is still cold:
  // the policy is about the prefetch of a screen the owner is not on.
  await app.goto("/companies?key=c%3Aacme&q=Beta");
  await app.expectRoute("companies");
  const search = app.page.locator("#global-search-input");
  await expect(search).toHaveValue("Beta");
  await search.fill("Bet");
  await search.fill("Beta");
  await expect(app.page).toHaveURL(/q=Beta/);
  await app.page.locator(".companies-row").filter({ hasText: "Beta" }).click();
  await expect(app.page).toHaveURL(/key=c%3Abeta/);
  await expect(app.page.locator("#companies-profile-title")).toHaveText("Beta");
  await expect(app.page.locator(".companies-row.selected")).toContainText("Beta");
  // Leave the shell search before hovering: a background refresh deferred for
  // a focused input must settle first, or its re-render masks the prefetch.
  await search.blur();
  await app.page.waitForTimeout(600);

  // The bare nav href is a different cache key from the filtered route, so
  // this is the prefetch that used to reset state.companyKeys and the query.
  await hoverNav(app.page, "companies");
  await hoverNav(app.page, "settings");

  await expect(search).toHaveValue("Beta");
  await expect(app.page.locator(".companies-row.selected")).toContainText("Beta");
  await expect(app.page.locator("#companies-profile-title")).toHaveText("Beta");

  // The selection index is resolved from state.companyKeys on a same-route
  // activation. A prefetched unfiltered ranking must not leave the filtered
  // row unselectable.
  await app.page.locator(".companies-row").filter({ hasText: "Beta" }).click();
  await expect(app.page.locator(".companies-row.selected")).toHaveCount(1);
  await expect(app.page.locator(".companies-row.selected")).toContainText("Beta");
  await expect(app.page.locator("#companies-profile-title")).toHaveText("Beta");
});

test("a failed route explains itself and Try again recovers", async ({ app }) => {
  const failure = { fail: true };
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/settings",
      handler: async ({ route }) => {
        if (failure.fail) {
          await fulfillJson(
            route,
            { error: { code: "internal", message: "temporary failure" } },
            500,
          );
          return;
        }
        await route.fallback();
      },
    },
  ]);

  await app.goto("/settings");
  await app.expectRoute("settings");
  await expect(app.page.locator("[data-error-screen]")).toBeVisible();

  failure.fail = false;
  await app.page.getByRole("button", { name: "Try again" }).click();
  await expect(app.page.locator("[data-error-screen]")).toBeHidden();
  await expect(app.page.locator("#main .screen").last()).toBeVisible();
  await expect(app.page.locator("#main")).toContainText("Settings");
});

test("the people table stays inside its wrapper at phone width", async ({ app }) => {
  await mockCompanies(app.page);
  await app.page.setViewportSize({ width: 360, height: 800 });
  await app.goto("/companies");
  await app.expectRoute("companies");
  await expect(app.page.locator(".companies-people-table")).toContainText(
    "Ada Lovelace",
  );

  const escapes = await app.page
    .locator(".companies-people-table td")
    .evaluateAll((cells) =>
      cells.flatMap((cell) => {
        const wrapper = cell.closest(".table-wrap");
        if (!wrapper) return ["a cell has no table wrapper"];
        const right = wrapper.getBoundingClientRect().right;
        return cell.getBoundingClientRect().right > right + 1
          ? [String(cell.textContent || "").trim().slice(0, 40)]
          : [];
      }),
    );
  expect(escapes).toEqual([]);
});
