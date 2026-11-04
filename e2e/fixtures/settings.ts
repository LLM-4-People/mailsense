import type { Page } from "@playwright/test";
import { fulfillJson, mockApi, type Mock } from "./mock";

// One synthetic settings catalog for the specs that read the Settings screen.
// It keeps the app's simple/full split and stays independent of the owner's
// real configuration; the request the client made, not the values, is what the
// tests assert.

export const simpleKeys = [
  "ui.language",
  "time.zone",
  "qa.enabled",
  "qa.max_messages",
  "sync.details.enabled",
  "triage.folder_roles",
  "threads.folder_roles",
  "learning.test_mode",
];

export function settingView(key: string, group: string): Record<string, unknown> {
  return {
    key,
    type: "string",
    default: "",
    group,
    description: `${key} description`,
    value: `${key}-value`,
    layer: "file",
    restart_pending: false,
  };
}

export const simpleGroups: Record<string, string> = {
  "ui.language": "Interface",
  "time.zone": "Interface",
  "qa.enabled": "Ask",
  "qa.max_messages": "Ask",
  "sync.details.enabled": "Mailboxes",
  "triage.folder_roles": "Mailboxes",
  "threads.folder_roles": "Mailboxes",
  "learning.test_mode": "Learning",
};

export const resourceSettings = [
  { ...settingView("resource.guard", "Resources"), type: "bool", value: true },
  { ...settingView("resource.cpu.max_percent", "Resources"), type: "int", value: 80, min: 10, max: 100 },
  { ...settingView("resource.ram.max_percent", "Resources"), type: "int", value: 85, min: 10, max: 100 },
  { ...settingView("resource.disk.max_percent", "Resources"), type: "int", value: 90, min: 50, max: 100 },
  { ...settingView("resource.sample_interval", "Resources"), type: "duration", value: "2s", min: "250ms", max: "30s" },
];

export const allSettings = [
  ...simpleKeys.map((key) => settingView(key, simpleGroups[key] ?? "General")),
  ...resourceSettings,
  settingView("http.proxy_url", "Network"),
  settingView("qa.temperature", "Ask"),
];
export const total = allSettings.length;
export const hiddenCount = total - simpleKeys.length;

export type SettingsFixture = {
  simpleQueries: string[];
  fullReads: number;
};

// mockSettings serves the real projection shape from the one synthetic catalog
// and records which read the client made. Extra routes share the same
// catch-all so a tab that also reads providers stays deterministic.
export async function mockSettings(
  page: Page,
  extra: Mock[] = [],
  language?: "en" | "fr",
): Promise<SettingsFixture> {
  const seen: SettingsFixture = { simpleQueries: [], fullReads: 0 };
  const view = (settings: Array<Record<string, unknown>>) => ({
    settings,
    total,
    restart_pending: [],
  });
  await mockApi(page, [
    ...(language
      ? [{
          method: "GET" as const,
          path: "/api/settings/ui.language",
          body: { key: "ui.language", value: language },
        }]
      : []),
    ...extra,
    {
      method: "GET",
      path: "/api/settings",
      handler: async ({ route, url }) => {
        const keys = url.searchParams.get("keys");
        if (keys) {
          seen.simpleQueries.push(keys);
          const wanted = new Set(keys.split(","));
          await fulfillJson(route, view(allSettings.filter((item) => wanted.has(String(item.key)))));
          return;
        }
        seen.fullReads += 1;
        await fulfillJson(route, view(allSettings));
      },
    },
  ]);
  return seen;
}
