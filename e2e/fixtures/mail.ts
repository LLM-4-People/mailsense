// Shared Mail wiring: the list, one message, its triage, the question
// catalogue, Ask history and Search. Specs that need a real Mail screen import
// this instead of rebuilding the same handler set, so a change to the Mail
// reads cannot leave one spec on an older contract.

import {
  messageListPage,
  messageView,
  priorityView,
  questionsView,
  searchView,
  triageResult,
  type MessagesPage,
} from "./data";
import { fulfillJson, mockApi } from "./mock";

export type MailMockOptions = {
  messagesPage?: (
    role: string,
    before: string,
    sender: string,
    unread: boolean,
  ) => MessagesPage;
  priorityPage?: (filter: string, limit: number, offset: number) => unknown;
  priorityFail?: () => boolean;
};

export async function mailMocks(
  page: Parameters<typeof mockApi>[0],
  options: MailMockOptions = {},
) {
  await mockApi(page, [
    {
      method: "GET",
      path: "/api/priority",
      handler: async ({ route, url }) => {
        const filter = url.searchParams.get("filter") || "all";
        const limit = url.searchParams.get("limit") || "50";
        const offset = Number(url.searchParams.get("offset") || "0");
        if (limit === "1") {
          const totals: Record<string, number> = {
            all: 2,
            reply: 1,
            deadline: 1,
          };
          await fulfillJson(route, {
            items: [],
            total: totals[filter] ?? 0,
            generated_at: "2026-10-25T10:00:00Z",
            preview: true,
          });
          return;
        }
        if (options.priorityPage) {
          if (options.priorityFail && options.priorityFail()) {
            await fulfillJson(route, { code: "internal", message: "Synthetic priority failure" }, 500);
            return;
          }
          await fulfillJson(route, options.priorityPage(filter, Number(limit), offset));
          return;
        }
        await fulfillJson(route, priorityView(filter));
      },
    },
    {
      method: "GET",
      path: "/api/messages",
      handler: async ({ route, url }) => {
        const role = url.searchParams.get("role") || "all";
        const before = url.searchParams.get("before") || "";
        const sender = url.searchParams.get("sender") || "";
        const unreadParam = (url.searchParams.get("unread") || "").toLowerCase();
        const unread = unreadParam === "true" || unreadParam === "1";
        await fulfillJson(
          route,
          options.messagesPage
            ? options.messagesPage(role, before, sender, unread)
            : messageListPage(role, before, sender, unread),
        );
      },
    },
    {
      method: "GET",
      path: /^\/api\/messages\/\d+$/,
      handler: async ({ route, url }) => {
        const id = Number(url.pathname.split("/").pop());
        await fulfillJson(route, messageView(id));
      },
    },
    {
      method: "GET",
      path: /^\/api\/triage\/\d+$/,
      handler: async ({ route, url }) => {
        const id = Number(url.pathname.split("/").pop());
        await fulfillJson(route, triageResult(id));
      },
    },
    { method: "GET", path: "/api/questions", body: questionsView() },
    { method: "GET", path: "/api/ask/history", body: { items: [] } },
    { method: "GET", path: "/api/search", body: searchView() },
  ]);
}
