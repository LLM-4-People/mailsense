import { expect, test, type Browser, type Page } from "@playwright/test";
import { mkdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fulfillJson, mockApi, type Mock } from "../fixtures/mock";
import { startServer, stopServer } from "../fixtures/server";
import {
  FIXED_NOW,
  askAnswerView,
  askHistoryView,
  agendaView,
  bootView,
  gateView,
  hubView,
  labelsQueueView,
  labelsSummaryView,
  learningView,
  mailboxView,
  messageListPage,
  messageView,
  priorityView,
  questionsView,
  settingsView,
  setupView,
  statusView,
  triageResult,
} from "./data";

// The captures are a separate Playwright project. Run them with:
//
//   cd e2e && npx playwright test --config screenshots/playwright.config.ts
//
// The screen list, the output directory and the two sizes come from the one
// media table in docs/site.json. Adding a screen there without a routine below
// fails the test.

type MediaTable = {
  screenshot_dir: string;
  screenshot_width: number;
  screenshot_height: number;
  social_preview: string;
  social_width: number;
  social_height: number;
  screenshots: Array<{ key: string; title: { en: string; fr: string } }>;
};

const repoRoot = path.resolve(__dirname, "..", "..");
const site = JSON.parse(
  readFileSync(path.join(repoRoot, "docs", "site.json"), "utf8"),
) as { media: MediaTable };
const media = site.media;
const outputDir = path.join(repoRoot, media.screenshot_dir);

const LANGUAGES = ["en", "fr"] as const;
type Lang = (typeof LANGUAGES)[number];

function assetName(key: string): string {
  return key.replace(/_/g, "-") + ".png";
}

function commonMocks(lang: Lang): Mock[] {
  return [
    { method: "GET", path: "/api/status", body: statusView(lang) },
    { method: "GET", path: "/api/settings/ui.language", body: { key: "ui.language", value: lang } },
    { method: "GET", path: "/api/gate", body: gateView() },
    { method: "GET", path: "/api/learning", body: learningView() },
    { method: "GET", path: "/api/hub", body: hubView() },
    { method: "GET", path: "/api/jobs", body: { jobs: [] } },
    { method: "GET", path: "/api/boot", body: bootView() },
    { method: "GET", path: "/api/ask/history", body: askHistoryView(lang) },
    { method: "GET", path: "/api/labels/summary", body: labelsSummaryView() },
    { method: "GET", path: "/api/setup", body: { needed: false } },
    { method: "GET", path: "/api/held-removals", body: { folders: [] } },
    { method: "GET", path: "/api/mailboxes", body: { mailboxes: [mailboxView(true)] } },
    // No provider, runtime or secret read falls through to the developer's
    // machine. The captures show the local index and nothing else.
    { method: "GET", path: "/api/providers", body: { providers: [], protocols: [], roles: [] } },
    { method: "GET", path: "/api/sidecar", body: { builds: [], runtimes: [] } },
  ];
}

async function open(page: Page, baseURL: string, hash: string, screen: string): Promise<void> {
  await page.goto(baseURL + hash);
  await expect(page.locator("#app")).toBeVisible();
  await expect(page.locator("#token-screen")).toBeHidden();
  await expect(page.locator("html")).toHaveAttribute("data-screen", screen);
}

const mailMocks = (): Mock[] => [
  { method: "GET", path: "/api/priority", body: priorityView() },
  { method: "GET", path: "/api/messages", body: messageListPage() },
  {
    method: "GET",
    path: /^\/api\/messages\/\d+$/,
    handler: async ({ route, url }) => {
      await fulfillJson(route, messageView(Number(url.pathname.split("/").pop())));
    },
  },
  {
    method: "GET",
    path: /^\/api\/triage\/\d+$/,
    handler: async ({ route, url }) => {
      await fulfillJson(route, triageResult(Number(url.pathname.split("/").pop())));
    },
  },
  { method: "GET", path: "/api/questions", body: questionsView() },
];

const teachMocks = (): Mock[] => [
  ...mailMocks(),
  {
    method: "GET",
    path: "/api/labels/queue",
    handler: async ({ route, url }) => {
      await fulfillJson(route, labelsQueueView(url.searchParams.get("question") || "category"));
    },
  },
];

const settingsMocks = (lang: Lang): Mock[] => [
  {
    method: "GET",
    path: "/api/settings/ui.language",
    body: { key: "ui.language", value: lang },
  },
  {
    method: "GET",
    path: "/api/settings",
    handler: async ({ route, url }) => {
      const body = settingsView(lang);
      const wanted = url.searchParams.get("keys");
      if (wanted) {
        const keys = new Set(wanted.split(","));
        body.settings = body.settings.filter((row) => keys.has(row.key));
      }
      await fulfillJson(route, body);
    },
  },
];

const setupMocks = (lang: Lang): Mock[] => [
  { method: "GET", path: "/api/setup", body: setupView(lang) },
  { method: "GET", path: "/api/settings", body: settingsView(lang) },
  { method: "GET", path: "/api/mailboxes", body: { mailboxes: [] } },
];

function captureRoutines(lang: Lang): Record<string, (page: Page, baseURL: string) => Promise<void>> {
  const withCommon = (extra: Mock[]) => [...extra, ...commonMocks(lang)];
  return {
    home: async (page, baseURL) => {
      await mockApi(page, withCommon([
        { method: "GET", path: "/api/priority", body: priorityView() },
        { method: "GET", path: "/api/agenda", body: agendaView() },
      ]));
      await open(page, baseURL, "#/hub", "hub");
      await expect(page.locator(".home-section-important .mail-row")).toHaveCount(3);
      await expect(page.locator(".home-section-agenda .agenda-row")).toHaveCount(6);
    },
    mail: async (page, baseURL) => {
      await mockApi(page, withCommon(mailMocks()));
      await open(page, baseURL, "#/mail?mode=all&id=4101", "mail");
      await expect(page.locator('.all-mail-row[data-message-id="4101"]')).toBeVisible();
      await expect(page.locator("#mail-detail .mail-subject")).toHaveText(
        "Signed proposal for the Northwind launch",
      );
      await expect(page.locator("#mail-detail .triage-facet-tags")).toContainText(
        lang === "fr" ? "Travail de projet" : "Project work",
      );
      await expect(page.locator("#mail-detail")).toContainText("signed copy of the proposal");
    },
    ask: async (page, baseURL) => {
      const question = lang === "fr"
        ? "Qui attend la proposition Northwind ?"
        : "Who is waiting on the Northwind proposal?";
      const answer = lang === "fr"
        ? "Trois fils attendent encore votre réponse."
        : "Three threads still need you.";
      await mockApi(page, withCommon([
        {
          method: "GET",
          path: "/api/ask/history/7",
          body: askAnswerView(lang, undefined, 7),
        },
        {
          method: "POST",
          path: "/api/ask",
          handler: async ({ route, request }) => {
            const body = request.postDataJSON() as { question?: string };
            await fulfillJson(route, askAnswerView(lang, body.question));
          },
        },
      ]));
      await open(page, baseURL, "#/ask", "ask");
      // Start a fresh conversation so the answer is current, not a saved
      // history entry with the "earlier answer" banner.
      await page.getByRole("button", {
        name: lang === "fr" ? "Nouvelle conversation" : "New conversation",
      }).click();
      await expect(page.locator(".ask-turn")).toHaveCount(0);
      await page.fill("#ask-input", question);
      await page.press("#ask-input", "Enter");
      await expect(page.locator(".ask-turn")).toHaveCount(1);
      await expect(page.locator(".ask-turn")).toContainText(answer);
      await expect(page.locator(".ask-citations")).toContainText(
        "Signed proposal for the Northwind launch",
      );
    },
    teach: async (page, baseURL) => {
      await mockApi(page, withCommon(teachMocks()));
      await open(page, baseURL, "#/labels?question=category", "labels");
      await expect(page.locator(".labels-task-layout")).toBeVisible();
      await expect(page.locator(".labels-task-subject")).toContainText("Invoice 1042 needs a second look");
      await expect(page.locator('input[name="answer-category"][value="security"]')).toBeAttached();
    },
    learning: async (page, baseURL) => {
      await mockApi(page, withCommon([]));
      await open(page, baseURL, "#/learning", "learning");
      await expect(page.locator(".learning-screen")).toBeVisible();
      await expect(page.locator('[data-stage-id="labels"]')).toContainText("812");
      await expect(page.locator('[data-stage-id="questions"]')).toBeVisible();
    },
    sources: async (page, baseURL) => {
      await mockApi(page, withCommon([]));
      await open(page, baseURL, "#/sources", "sources");
      await expect(page.locator(".sources-mailbox-table")).toBeVisible();
      await expect(page.locator(".sources-mailbox-table")).toContainText("Outlook");
      await expect(page.locator(".sources-registry-table")).toContainText("outlook");
    },
    settings: async (page, baseURL) => {
      await mockApi(page, withCommon(settingsMocks(lang)));
      await open(page, baseURL, "#/settings", "settings");
      await expect(page.locator(".settings-screen")).toBeVisible();
      await expect(page.locator(".setting-row")).toHaveCount(8);
      await expect(page.locator(".setting-row").first()).toBeVisible();
    },
    first_run: async (page, baseURL) => {
      await mockApi(page, withCommon(setupMocks(lang)));
      await page.goto(baseURL);
      await expect(page.locator("#setup-root")).toBeVisible();
      await expect(page.locator("#app")).toBeHidden();
      await expect(page.locator("#setup-step-title")).toHaveText(
        lang === "fr" ? "Bienvenue" : "Welcome",
      );
      await expect(page.locator("#setup-root")).toContainText("/home/sam/.local/share/mailsense");
    },
  };
}

// settle waits for the real content, hides the chrome that would differ between
// runs, and refuses to capture a loading skeleton or a read error.
async function settle(page: Page, lang: Lang): Promise<void> {
  await expect(page.locator('[data-nav="hub"]')).toContainText(lang === "fr" ? "Accueil" : "Home");
  await page.evaluate(() => document.fonts.ready.then(() => true));
  // The page CSP forbids inline style tags, and a late boot poll can repaint a
  // hidden strip. Removing the transient chrome keeps the real document and
  // its real stylesheet, and nothing can bring the strip back.
  await page.evaluate(() => {
    for (const id of ["boot-strip", "job-strip", "toasts", "connection", "topbar-update"]) {
      document.getElementById(id)?.remove();
    }
  });
  const bad = page.locator(
    ".skeleton, .skeleton-line, .agenda-skeleton, .brief-skeleton, .sources-skeleton, " +
      ".mail-detail-skeleton, .thread-skeleton, .conversation-skeleton, .companies-skeleton-list, " +
      ".inline-read-error, .conversation-error, .pager-error, .spinner",
  );
  await expect(bad.filter({ visible: true })).toHaveCount(0);
  await page.evaluate(() => {
    const active = document.activeElement;
    if (active instanceof HTMLElement) active.blur();
    window.scrollTo(0, 0);
    for (const node of document.querySelectorAll<HTMLElement>(".section-scroll, #main")) {
      node.scrollTop = 0;
    }
  });
}

test("capture every documented screen in both languages", async ({ browser }) => {
  const server = await startServer();
  try {
    for (const lang of LANGUAGES) {
      const routines = captureRoutines(lang);
      const context = await browser.newContext({
        viewport: { width: media.screenshot_width, height: media.screenshot_height },
        deviceScaleFactor: 1,
        locale: lang === "fr" ? "fr-FR" : "en-US",
        timezoneId: "Europe/Paris",
        colorScheme: "light",
        reducedMotion: "reduce",
      });
      await context.addInitScript(
        ({ token }) => {
          try {
            sessionStorage.setItem("mailsense.token", token);
          } catch {
            // An opaque origin cannot use storage.
          }
        },
        { token: server.token },
      );

      for (const shot of media.screenshots) {
        const routine = routines[shot.key];
        if (!routine) throw new Error("no capture routine for screen " + shot.key);
        const page = await context.newPage();
        // The date is fixed so relative times, "generated" stamps and the
        // learning clock never differ between runs.
        await page.clock.setFixedTime(new Date(FIXED_NOW));
        await routine(page, server.baseURL);
        await settle(page, lang);
        const target = path.join(outputDir, lang, assetName(shot.key));
        mkdirSync(path.dirname(target), { recursive: true });
        await page.screenshot({ path: target, animations: "disabled", caret: "hide" });
        await page.close();
      }
      await context.close();
    }

    await captureSocialPreview(browser);
  } finally {
    await stopServer(server);
  }
});

// captureSocialPreview renders the generated brand banner at the card size.
// The PNG is a real asset in the repository; the site head points at it.
async function captureSocialPreview(browser: Browser): Promise<void> {
  const source = path.join(repoRoot, "docs", "assets", "banner-en.svg");
  const target = path.join(repoRoot, media.social_preview);
  mkdirSync(path.dirname(target), { recursive: true });
  const context = await browser.newContext({
    viewport: { width: media.social_width, height: media.social_height },
    deviceScaleFactor: 1,
    locale: "en-US",
    timezoneId: "Europe/Paris",
    colorScheme: "light",
  });
  const page = await context.newPage();
  // A standalone SVG document has no HTML element to paint a background, so
  // Chrome letterboxes it on black. Render it inside one exact-size page
  // instead, which also removes any scrollbar or aspect-ratio gap.
  const svgMarkup = readFileSync(source, "utf8");
  await page.setContent(
    `<!doctype html><html><head><meta charset="utf-8"><style>
      html, body { margin: 0; padding: 0; background: #f7f3ea; }
      svg { display: block; width: ${media.social_width}px; height: ${media.social_height}px; }
    </style></head><body>${svgMarkup}</body></html>`,
    { waitUntil: "load" },
  );
  // Capture the element, not the page, so no scrollbar or page background can
  // add a border around the card.
  await page.locator("svg").screenshot({ path: target, animations: "disabled" });
  await context.close();
}
