import { learningView } from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { installFakeEventSource } from "../fixtures/sse";
import { expect, test } from "../fixtures/test";

const PRIMARY_ROUTES = [
  "hub",
  "brief",
  "mail",
  "companies",
  "ask",
  "labels",
  "sources",
  "settings",
  "logs",
  "learning",
];

test.describe("shell and transport", () => {
  test("consumes the launch token and never leaves it in the URL or DOM", async ({
    app,
  }) => {
    await app.goto("/hub");
    await app.expectRoute("hub");
    await app.expectLive();

    const stored = await app.page.evaluate(() =>
      sessionStorage.getItem("mailsense.token"),
    );
    expect(stored).toBe(app.server.token);
    expect(app.page.url()).not.toContain("token=");
    expect(await app.page.content()).not.toContain(app.server.token);
  });

  test("a rejected token returns to the reopen screen", async ({ app }) => {
    await app.page.goto(app.server.baseURL + "#token=" + "z".repeat(43));
    await expect(app.page.locator("#token-screen")).toBeVisible();
    await expect(app.page.locator("#app")).toBeHidden();
    await expect(app.page.locator("#token-title")).toHaveText("Reopen the app");
    expect(app.page.url()).not.toContain("token=");
    expect(
      await app.page.evaluate(() => sessionStorage.getItem("mailsense.token")),
    ).toBeNull();
  });

  test("every primary route mounts without a page error", async ({ app }) => {
    const pageErrors: string[] = [];
    app.page.on("pageerror", (error) => pageErrors.push(error.message));
    for (const route of PRIMARY_ROUTES) {
      await app.goto("/" + route);
      await app.expectRoute(route);
      await expect(app.page.locator("#main .screen").last()).toBeVisible();
      await expect(app.page.locator("#screen-title")).not.toBeEmpty();
    }
    expect(pageErrors).toEqual([]);
  });

  test("the shell never scrolls; route sections own the overflow", async ({
    app,
  }) => {
    // One navigation per route, then a resize per viewport: the old order
    // reloaded every route for every width (70 navigations for 10 routes).
    for (const route of PRIMARY_ROUTES) {
      await app.goto("/" + route);
      await app.expectRoute(route);
      await expect(app.page.locator("#main .screen").last()).toBeVisible();
      for (const viewport of [
        { width: 1920, height: 1080 },
        { width: 1440, height: 900 },
        { width: 1280, height: 800 },
        { width: 1025, height: 800 },
        { width: 1024, height: 800 },
        { width: 768, height: 800 },
        { width: 360, height: 800 },
      ]) {
        await app.page.setViewportSize(viewport);
        // Two frames: the resize and the layout it triggers settle before the
        // probe reads computed styles, without a fixed 60 ms sleep.
        await app.page.evaluate(
          () =>
            new Promise<void>((resolve) =>
              requestAnimationFrame(() =>
                requestAnimationFrame(() => resolve()),
              ),
            ),
        );
        const probe = await app.page.evaluate(() => {
          const main = document.querySelector("#main") as HTMLElement;
          const screen = main.querySelector(".screen") as HTMLElement;
          const doc = document.documentElement;
          const sections = Array.from(
            main.querySelectorAll<HTMLElement>("[data-scroll-key]"),
          ).map((node) => ({
            key: node.dataset.scrollKey || "",
            overflowY: getComputedStyle(node).overflowY,
            client: node.clientHeight,
            scroll: node.scrollHeight,
            horizontal: node.scrollWidth - node.clientWidth,
          }));
          return {
            mainOverflowY: getComputedStyle(main).overflowY,
            mainClient: main.clientHeight,
            mainScroll: main.scrollHeight,
            screenOverflowY: screen ? getComputedStyle(screen).overflowY : "",
            screenClient: screen ? screen.clientHeight : 0,
            screenScroll: screen ? screen.scrollHeight : 0,
            screenWidth: screen ? screen.scrollWidth - screen.clientWidth : 0,
            mainWidth: main.scrollWidth - main.clientWidth,
            documentClient: doc.clientHeight,
            documentScroll: doc.scrollHeight,
            documentWidth: doc.scrollWidth - doc.clientWidth,
            sections,
          };
        });
        expect(probe.mainOverflowY, route + " scrolls #main").toBe("hidden");
        expect(
          probe.mainScroll,
          route + " overflows #main at " + viewport.width + "px",
        ).toBeLessThanOrEqual(probe.mainClient + 1);
        expect(
          probe.documentScroll,
          route + " scrolls the document at " + viewport.width + "px",
        ).toBeLessThanOrEqual(probe.documentClient + 1);
        expect(
          probe.documentWidth,
          route + " document overflows sideways",
        ).toBeLessThanOrEqual(1);
        expect(
          probe.mainWidth,
          route + " overflows #main sideways",
        ).toBeLessThanOrEqual(1);
        expect(
          probe.screenWidth,
          route + " route body overflows sideways",
        ).toBeLessThanOrEqual(1);
        if (viewport.width >= 1025) {
          expect(
            probe.screenOverflowY,
            route +
              " still scrolls the whole route at " +
              viewport.width +
              "px",
          ).not.toBe("auto");
          expect(
            probe.screenScroll,
            route + " route body overflows at " + viewport.width + "px",
          ).toBeLessThanOrEqual(probe.screenClient + 1);
          for (const section of probe.sections) {
            expect(
              section.overflowY,
              route + " section " + section.key + " is not bounded",
            ).toBe("auto");
            expect(
              section.horizontal,
              route + " section " + section.key + " scrolls sideways",
            ).toBeLessThanOrEqual(1);
          }
        } else {
          expect(
            probe.screenOverflowY,
            route + " narrow fallback is not the section scroller",
          ).toBe("auto");
        }
      }
    }
  });

  test("long content scrolls inside its own section, not the shell", async ({
    app,
  }) => {
    const owners: Record<string, string> = {
      hub: '[data-scroll-key="home-brief"]',
      brief: "#brief-must .brief-body",
      mail: ".mail-list",
      companies: ".companies-profile",
      ask: ".ask-main",
      sources: ".route-scroll",
      settings: ".settings-list",
      logs: ".logs",
      learning: ".learning-body",
      answers: ".answers-body",
    };
    await app.page.setViewportSize({ width: 1280, height: 800 });
    for (const [route, selector] of Object.entries(owners)) {
      await app.goto("/" + route);
      await app.expectRoute(route);
      // The route marker paints before async content mounts; wait for the
      // section itself or the filler append runs against a null node.
      await app.page.locator(selector).first().waitFor({ state: "attached" });
      const probe = await app.page.evaluate((owner) => {
        const section = document.querySelector(owner) as HTMLElement;
        const filler = document.createElement("div");
        filler.style.height = "3000px";
        filler.setAttribute("data-scroll-filler", "1");
        section.append(filler);
        const main = document.querySelector("#main") as HTMLElement;
        const doc = document.documentElement;
        return {
          overflowY: getComputedStyle(section).overflowY,
          client: section.clientHeight,
          scroll: section.scrollHeight,
          mainClient: main.clientHeight,
          mainScroll: main.scrollHeight,
          documentClient: doc.clientHeight,
          documentScroll: doc.scrollHeight,
        };
      }, selector);
      expect(probe.overflowY, route + " section is not scrollable").toBe(
        "auto",
      );
      expect(
        probe.scroll,
        route + " section did not take the long content",
      ).toBeGreaterThan(probe.client + 1);
      expect(
        probe.mainScroll,
        route + " long section grew #main",
      ).toBeLessThanOrEqual(probe.mainClient + 1);
      expect(
        probe.documentScroll,
        route + " long section grew the document",
      ).toBeLessThanOrEqual(probe.documentClient + 1);
    }
  });

  // Every Home column is the same primitive: one title size, and a body that
  // owns the column's scroll. The middle column used to grow past the grid
  // and paint under the More disclosure because its wrapper had no bounded
  // height; this proves all three are bounded and scroll instead.
  test("every home column is a bounded scroller with one title size", async ({
    app,
  }) => {
    await app.page.setViewportSize({ width: 1280, height: 800 });
    await app.goto("/hub");
    await app.expectRoute("hub");
    await expect(app.page.locator(".home-section").first()).toBeVisible();
    // The three columns paint as their reads land: measure only once every
    // column's body exists, or a slow run measures two of them and fails.
    await expect(
      app.page.locator(".home-brief-grid .home-section-body[data-scroll-key]"),
    ).toHaveCount(3);
    const probe = await app.page.evaluate(() => {
      const grid = document.querySelector(".home-brief-grid") as HTMLElement;
      const sections = Array.from(
        document.querySelectorAll(
          ".home-brief-grid > .home-section, .home-brief-grid > .home-section-host > .home-section",
        ),
      ) as HTMLElement[];
      const titles = sections.map((section) => {
        const heading = section.querySelector(
          ".home-section-head h2",
        ) as HTMLElement;
        return {
          text: heading.textContent,
          size: getComputedStyle(heading).fontSize,
        };
      });
      const bodies = sections.map((section) => {
        const body = section.querySelector(".home-section-body") as HTMLElement;
        const filler = document.createElement("div");
        filler.style.height = "3000px";
        body.append(filler);
        return {
          key: body.dataset.scrollKey,
          overflow: getComputedStyle(body).overflowY,
          client: body.clientHeight,
          scroll: body.scrollHeight,
          bottom: body.getBoundingClientRect().bottom,
        };
      });
      return {
        titles,
        bodies,
        gridBottom: grid.getBoundingClientRect().bottom,
      };
    });
    expect(probe.titles.length, "home columns").toBe(3);
    const sizes = new Set(probe.titles.map((title) => title.size));
    expect(
      sizes.size,
      "home section titles differ: " + JSON.stringify(probe.titles),
    ).toBe(1);
    for (const body of probe.bodies) {
      expect(body.overflow, body.key + " is not a scroller").toBe("auto");
      expect(
        body.scroll,
        body.key + " did not take the long content",
      ).toBeGreaterThan(body.client + 1);
      expect(
        body.bottom,
        body.key + " paints past the column grid",
      ).toBeLessThanOrEqual(probe.gridBottom + 1);
    }
  });

  // One cell primitive: the value-first Home counts, the link cells of the
  // Brief and the company KPIs are built by the same statItem/statStrip, so
  // each keeps a value and a human label instead of drifting apart.
  test("the Home and Brief stat strips share one cell", async ({ app }) => {
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/learning",
        body: learningView({ open: true, preview: false }),
      },
      { method: "GET", path: "/api/priority", body: { items: [], total: 0 } },
      {
        method: "GET",
        path: "/api/agenda",
        body: { obligations: 0, deadlines: 0, events: 0, horizon_hours: 24 },
      },
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
    const home = app.page.locator(".home-head-counts .stat");
    await expect(home).toHaveCount(3);
    for (const cell of await home.all()) {
      await expect(cell.locator(".stat-label")).not.toBeEmpty();
      await expect(cell.locator(".stat-value")).not.toBeEmpty();
    }
    await app.goto("/brief");
    await app.expectRoute("brief");
    const brief = app.page.locator(".brief-stats .stat");
    await expect(brief).toHaveCount(4);
    for (const cell of await brief.all()) {
      await expect(cell.locator(".stat-label")).not.toBeEmpty();
      await expect(cell).toHaveAttribute("href", /^#brief-/);
    }
  });

  // The Home recent rail is a supplementary read: a failure is a retry line
  // in that rail, never "no recent activity", and the retry redraws in place.
  test("a failed ask history retries instead of claiming no recent activity", async ({
    app,
  }) => {
    let historyFail = true;
    await mockApi(app.page, [
      {
        method: "GET",
        path: "/api/learning",
        body: learningView({ open: true, preview: false }),
      },
      { method: "GET", path: "/api/hub", body: {} },
      { method: "GET", path: "/api/priority", body: { items: [], total: 0 } },
      {
        method: "GET",
        path: "/api/agenda",
        body: { obligations: 0, deadlines: 0, events: 0, horizon_hours: 24 },
      },
      {
        method: "GET",
        path: "/api/ask/history",
        handler: async ({ route }) => {
          if (historyFail) {
            await fulfillJson(
              route,
              {
                error: {
                  code: "internal",
                  message: "synthetic history failure",
                },
              },
              500,
            );
            return;
          }
          await fulfillJson(route, {
            items: [
              {
                id: 7,
                question: "When is the synthetic invoice due?",
                mode: "retrieval",
                created_at: "2026-10-25T10:00:00Z",
              },
            ],
          });
        },
      },
    ]);

    await app.goto("/hub");
    await app.expectRoute("hub");
    await app.page.locator(".home-more > summary").click();
    const recent = app.page.locator(
      "section.home-section:has(#home-more-recent-title) .home-section-body",
    );
    await expect(recent).toContainText("Recent questions could not be read.");
    await expect(recent).not.toContainText("No recent activity yet.");

    historyFail = false;
    await recent.getByRole("button", { name: "Try again" }).click();

    await expect(recent).not.toContainText(
      "Recent questions could not be read.",
    );
    await expect(recent).toContainText("When is the synthetic invoice due?");
  });

  test("a route section restores its own scroll from the DOM cache", async ({
    app,
  }) => {
    // This spec owns the router's DOM cache, not live refresh: a boot event
    // that refreshes the active screen replaces the node it just scrolled.
    await installFakeEventSource(app.page);
    await app.page.setViewportSize({ width: 1280, height: 800 });
    await app.goto("/hub");
    await app.expectRoute("hub");
    await expect(
      app.page.locator('[data-scroll-key="home-brief"]'),
    ).toBeVisible();
    await app.page.evaluate(() => {
      const brief = document.querySelector(
        '[data-scroll-key="home-brief"]',
      ) as HTMLElement;
      const filler = document.createElement("div");
      filler.style.height = "3000px";
      brief.append(filler);
      brief.scrollTop = 210;
    });
    await app.page.locator('[data-nav="mail"]').click();
    await app.expectRoute("mail");
    await app.page.locator('[data-nav="hub"]').click();
    await app.expectRoute("hub");
    // Poll rather than read once: the assertion is the restored position, and
    // a mount that is one frame behind must not fail the test. A screen that
    // was re-rendered instead of restored still fails, because the injected
    // filler does not exist in a freshly built node.
    await expect
      .poll(
        () =>
          app.page.evaluate(() => {
            const brief = document.querySelector(
              '[data-scroll-key="home-brief"]',
            ) as HTMLElement | null;
            const main = document.querySelector("#main") as HTMLElement | null;
            return {
              brief: brief ? brief.scrollTop : -1,
              main: main ? main.scrollTop : -1,
            };
          }),
        { message: "the cached route did not restore its section scroll" },
      )
      .toEqual({ brief: 210, main: 0 });
  });

  test("the narrow route body restores its own scroll from the DOM cache", async ({
    app,
  }) => {
    // This spec owns the router's DOM cache, not live refresh: a boot event
    // that refreshes the active screen replaces the node it just scrolled.
    await installFakeEventSource(app.page);
    await app.page.setViewportSize({ width: 360, height: 800 });
    await app.goto("/settings");
    await app.expectRoute("settings");
    // The settings body must be tall enough to hold the offset before it is
    // set: a slow render clamps scrollTop and the restore below reads 0.
    await expect
      .poll(
        () =>
          app.page.evaluate(() => {
            const screen = document.querySelector(
              "#main .screen",
            ) as HTMLElement;
            return screen ? screen.scrollHeight - screen.clientHeight : 0;
          }),
        { message: "the settings body never became tall enough to scroll" },
      )
      .toBeGreaterThanOrEqual(300);
    await app.page.evaluate(() => {
      const screen = document.querySelector("#main .screen") as HTMLElement;
      screen.scrollTop = 300;
    });
    // At phone width the sidebar is a drawer that closes on every route
    // selection, so each navigation opens it first.
    const openDrawer = async () => {
      const toggle = app.page.locator("#nav-toggle");
      if (await toggle.isVisible()) {
        if ((await toggle.getAttribute("aria-expanded")) !== "true") {
          await toggle.click();
        }
        await expect(toggle).toHaveAttribute("aria-expanded", "true");
      }
    };
    await openDrawer();
    await app.page.locator('[data-nav="mail"]').click();
    await app.expectRoute("mail");
    await openDrawer();
    await app.page.locator('[data-nav="settings"]').click();
    await app.expectRoute("settings");
    const restored = await app.page.evaluate(() => {
      const screen = document.querySelector("#main .screen") as HTMLElement;
      const main = document.querySelector("#main") as HTMLElement;
      return { screen: screen.scrollTop, main: main.scrollTop };
    });
    expect(restored.screen).toBe(300);
    expect(restored.main).toBe(0);
  });

  test("no primary route scrolls sideways at phone width", async ({ app }) => {
    await app.page.setViewportSize({ width: 360, height: 800 });
    for (const route of PRIMARY_ROUTES) {
      await app.goto("/" + route);
      await app.expectRoute(route);
      await expect(app.page.locator("#main .screen").last()).toBeVisible();
      const probe = await app.page.evaluate(() => {
        const doc = document.documentElement;
        const offenders: string[] = [];
        document.querySelectorAll("*").forEach((element) => {
          // The Mail folder strip is a deliberate one-row scroller on narrow
          // screens; it must never grow the document itself.
          if (element.closest(".filter-chips")) return;
          const style = getComputedStyle(element);
          if (
            (style.overflowX === "auto" || style.overflowX === "scroll") &&
            element.scrollWidth > element.clientWidth + 1
          ) {
            offenders.push(
              element.tagName + "." + String(element.className || ""),
            );
          }
        });
        return {
          documentOverflow: doc.scrollWidth - doc.clientWidth,
          offenders,
        };
      });
      expect(
        probe.documentOverflow,
        route + " document overflows at 360px",
      ).toBeLessThanOrEqual(1);
      expect(
        probe.offenders,
        route + " has horizontal scrollers at 360px",
      ).toEqual([]);
    }
  });

  test("home wraps long agenda content at every width", async ({ app }) => {
    const longTitle =
      "Réunion de coordination avec la direction régionale et tous les partenaires du projet de modernisation";
    await mockApi(app.page, [
      { method: "GET", path: "/api/learning", body: learningView() },
      {
        method: "GET",
        path: "/api/agenda",
        body: {
          generated_at: "2026-10-29T12:00:00Z",
          events: 1,
          obligations: 0,
          deadlines: 0,
          backlog: 0,
          items: [
            {
              kind: "event",
              at: "2026-10-29T15:00:00Z",
              title: "Revue trimestrielle",
              location:
                "Salle de conférence du siège social, 1234 boulevard Industriel, Trois-Rivières",
              linked_event_title: longTitle,
            },
          ],
        },
      },
    ]);
    await app.goto("/hub");
    await app.expectRoute("hub");
    await expect(app.page.locator("#main .home-screen")).toBeVisible();
    await expect(
      app.page.locator(".home-section-agenda .agenda-meta .badge"),
    ).toContainText("Réunion de coordination");
    for (const width of [360, 420, 768, 1024, 1280, 1440]) {
      await app.page.setViewportSize({ width, height: 900 });
      const probe = await app.page.evaluate(() => {
        const main = document.querySelector("#main") as HTMLElement;
        const bounds = main.getBoundingClientRect();
        const offenders: string[] = [];
        for (const element of Array.from(main.querySelectorAll("*"))) {
          const node = element as HTMLElement;
          const style = getComputedStyle(node);
          if (style.display === "none" || style.visibility === "hidden")
            continue;
          const rect = node.getBoundingClientRect();
          if (rect.width === 0 && rect.height === 0) continue;
          if (rect.left < bounds.left - 2 || rect.right > bounds.right + 2) {
            offenders.push(node.tagName + "." + String(node.className || ""));
          }
        }
        return {
          mainOverflow: main.scrollWidth - main.clientWidth,
          offenders,
        };
      });
      expect(
        probe.mainOverflow,
        "home overflows at " + width + "px",
      ).toBeLessThanOrEqual(1);
      expect(
        probe.offenders,
        "home content escapes #main at " + width + "px",
      ).toEqual([]);
    }
  });

  test("narrow windows keep every route reachable", async ({ app }) => {
    for (const width of [700, 390]) {
      await app.page.setViewportSize({ width, height: 800 });
      await app.goto("/hub");
      await app.expectRoute("hub");
      await expect(app.page.locator(".sidebar")).toBeVisible();
      await expect(app.page.locator(".sidebar .nav-item")).toHaveCount(11);

      const shutdown = app.page.locator("#shutdown-button");
      await expect(shutdown).toBeVisible();
      await expect(shutdown).toHaveAccessibleName(/Shut down|Éteindre/);

      // Below the drawer breakpoint the sidebar starts closed: the menu
      // toggle is the one way in, so a route is still reachable.
      const toggle = app.page.locator("#nav-toggle");
      if (await toggle.isVisible()) {
        await expect(toggle).toHaveAttribute("aria-expanded", "false");
        await toggle.click();
        await expect(toggle).toHaveAttribute("aria-expanded", "true");
      }

      const settings = app.page.locator('.sidebar [data-nav="settings"]');
      await expect(settings).toBeVisible();
      await settings.click();
      await app.expectRoute("settings");
      await expect(app.page.locator("#main .screen").last()).toBeVisible();
    }
  });

  test("the shell only talks to its own loopback origin", async ({ app }) => {
    const external: string[] = [];
    app.page.on("request", (request) => {
      const url = request.url();
      if (
        !url.startsWith(app.server.baseURL) &&
        !url.startsWith("data:") &&
        !url.startsWith("about:") &&
        !url.startsWith("blob:")
      ) {
        external.push(url);
      }
    });
    for (const route of ["hub", "mail", "companies", "ask", "logs"]) {
      await app.goto("/" + route);
      await app.expectRoute(route);
    }
    expect(external).toEqual([]);
  });
});

test.describe("boot contract", () => {
  test("first route skeleton mounts before status and learning", async ({
    app,
  }) => {
    let statusPending = 0;
    let releaseStatus!: () => void;
    const statusGate = new Promise<void>((resolve) => {
      releaseStatus = resolve;
    });
    let releaseLearning!: () => void;
    const learningGate = new Promise<void>((resolve) => {
      releaseLearning = resolve;
    });
    await app.page.route("**/api/status", async (route) => {
      statusPending += 1;
      await statusGate;
      await route.continue();
    });
    await app.page.route("**/api/learning", async (route) => {
      await learningGate;
      await route.continue();
    });

    await app.page.goto(app.server.baseURL + "#/hub");
    await expect(app.page.locator("#app")).toBeVisible();
    const skeleton = app.page
      .locator('#main .screen[aria-busy="true"]')
      .first();
    await expect(skeleton).toBeVisible();
    await expect(
      app.page.locator('#main .screen[aria-busy="true"]'),
    ).toHaveCount(1);
    // A live invalidation (the warm's index.finished) may legitimately
    // replace the first in-flight status read. The contract here is that the
    // skeleton is already painted while status is still unanswered.
    expect(statusPending).toBeGreaterThanOrEqual(1);

    releaseStatus();
    releaseLearning();
    await app.expectRoute("hub");
    await expect(
      app.page.locator('#main .screen[aria-busy="true"]'),
    ).toHaveCount(0);
  });

  test("boot strip reports live progress", async ({ app }) => {
    let calls = 0;
    await app.page.route("**/api/boot", async (route) => {
      calls += 1;
      if (calls <= 2) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            state: "warming",
            done: 3,
            total: 12,
            failed: 0,
            current: "priority",
            views: [],
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          state: "ready",
          done: 11,
          total: 11,
          failed: 0,
          current: "",
          views: [],
        }),
      });
    });

    await app.page.goto(app.server.baseURL + "#/hub");
    const strip = app.page.locator("#boot-strip");
    await expect(strip).toBeVisible();
    await expect(strip).toHaveAttribute("role", "status");
    const progress = strip.locator("progress");
    await expect(progress).toHaveAttribute("max", "12");
    await expect(progress).toHaveAttribute("value", "3");
    await expect(strip.locator(".boot-strip-count")).toHaveText("3/12");
    await expect(strip.locator(".boot-strip-current")).toHaveText(
      "Now: Priority ranking",
    );
    await expect(strip).toBeHidden();
    expect(calls).toBeGreaterThan(2);
  });

  test("ready with failed views keeps a dismissible warning", async ({
    app,
  }) => {
    await app.page.route("**/api/boot", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          state: "ready",
          done: 11,
          total: 12,
          failed: 1,
          current: "",
          views: [
            { name: "triage_page", state: "failed", duration_ms: 4 },
            { name: "semantic_search", state: "done", duration_ms: 2 },
          ],
        }),
      });
    });

    await app.page.goto(app.server.baseURL + "#/hub");
    const strip = app.page.locator("#boot-strip");
    await expect(strip).toBeVisible();
    await expect(strip.locator(".boot-strip-title")).toHaveText(
      "Ready, but 1 start-up view failed",
    );
    await expect(strip.locator(".boot-strip-count")).toHaveText("11/12");
    await expect(strip.locator(".boot-strip-current")).toHaveText(
      "Failed: Triage page",
    );
    await strip.getByRole("button", { name: "Dismiss" }).click();
    await expect(strip).toBeHidden();
  });
});
