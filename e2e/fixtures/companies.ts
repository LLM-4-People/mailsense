import type { Page } from "@playwright/test";
import { companiesView, companyProfile } from "./data";
import { fulfillJson, mockApi } from "./mock";

// mockCompanies is the one Companies fixture: the list view, the empty profile
// summary the screen reads on mount, and the three profile keys the specs
// select. A test that needs a different list or profile map registers its own
// routes instead of copying this one.
export async function mockCompanies(page: Page): Promise<void> {
  await mockApi(page, [
    { method: "GET", path: "/api/companies", body: companiesView() },
    {
      method: "GET",
      path: "/api/companies/profiles",
      body: { generated_at: "2026-10-25T10:00:00Z", profiles: {} },
    },
    {
      method: "GET",
      path: /^\/api\/companies\/.+$/,
      handler: async ({ route, url }) => {
        const key = decodeURIComponent(url.pathname.split("/").pop() || "");
        for (const [id, label, domain] of [
          ["c:acme", "Acme", "acme.test"],
          ["c:beta", "Beta", "beta.test"],
          ["c:external", "External Co", "external.test"],
        ]) {
          if (key === id) {
            await fulfillJson(route, companyProfile(key, label, domain));
            return;
          }
        }
        await fulfillJson(route, { code: "unknown_company" }, 404);
      },
    },
  ]);
}
