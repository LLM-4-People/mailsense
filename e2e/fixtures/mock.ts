import type { Page, Request, Route } from "@playwright/test";

export type MockContext = {
  route: Route;
  url: URL;
  request: Request;
};

export type Mock = {
  method?: "GET" | "POST" | "PUT" | "DELETE";
  /** Exact pathname, or a regular expression over the pathname. */
  path: string | RegExp;
  /** All listed query parameters must match exactly. */
  query?: Record<string, string | undefined>;
  status?: number;
  body?: unknown;
  handler?: (ctx: MockContext) => Promise<void> | void;
};

function matches(pattern: string | RegExp, pathname: string): boolean {
  return typeof pattern === "string" ? pattern === pathname : pattern.test(pathname);
}

/**
 * Install one catch-all API router on the page. Responses listed here are
 * synthetic; every other request falls through to the real Go server, so
 * auth, CSP, static caching and SSE keep their production behavior.
 */
export async function mockApi(page: Page, mocks: Mock[]): Promise<void> {
  // A learning mock describes the gate the test intends. Unless the test
  // overrides it, answer /api/gate from the same state so every screen reads
  // one permission snapshot; production reads the real gate route.
  const all = [...mocks];
  const hasGate = all.some((mock) => (mock.method ?? "GET") === "GET" && mock.path === "/api/gate");
  const learning = all.find((mock) => (mock.method ?? "GET") === "GET" &&
    mock.path === "/api/learning" && mock.body && !mock.handler);
  if (!hasGate && learning) {
    const view = learning.body as { open?: boolean; preview?: boolean };
    all.push({
      method: "GET",
      path: "/api/gate",
      body: { allowed: !!(view.open || view.preview), open: !!view.open, preview: !!view.preview },
    });
  }
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    for (const mock of all) {
      if (mock.method && mock.method !== request.method()) continue;
      if (!matches(mock.path, url.pathname)) continue;
      if (
        mock.query &&
        !Object.entries(mock.query).every(
          ([key, value]) => (url.searchParams.get(key) ?? undefined) === value,
        )
      ) {
        continue;
      }
      if (mock.handler) {
        await mock.handler({ route, url, request });
        return;
      }
      await route.fulfill(json(mock.body, mock.status));
      return;
    }
    await route.fallback();
  });
}

export function json(body: unknown, status = 200) {
  return {
    status,
    contentType: "application/json",
    body: JSON.stringify(body ?? {}),
  };
}

export async function fulfillJson(route: Route, body: unknown, status = 200): Promise<void> {
  await route.fulfill(json(body, status));
}
