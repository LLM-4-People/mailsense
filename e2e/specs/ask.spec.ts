import { agendaView, askView, clarifyView, topicContactsView } from "../fixtures/data";
import { fulfillJson, mockApi } from "../fixtures/mock";
import { expect, test } from "../fixtures/test";

type AskRequest = { question: string; language: string; conversation_id?: string; route?: string };

test.describe("Ask conversation", () => {
  test("a follow-up appends to the same transcript and carries the conversation id", async ({
    app,
  }) => {
    const requests: AskRequest[] = [];
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route, request }) => {
          const body = request.postDataJSON() as AskRequest;
          requests.push(body);
          const turn = requests.length;
          await fulfillJson(
            route,
            askView({
              id: turn,
              question: body.question,
              conversation_id: "conv-e2e",
              answer:
                turn === 1
                  ? "First synthetic answer about the invoice."
                  : "Second synthetic answer, using the same conversation.",
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.expectRoute("ask");

    await app.page.fill("#ask-input", "Summarize the synthetic invoice thread");
    await app.page.press("#ask-input", "Enter");
    await expect(app.page.locator(".ask-turn")).toHaveCount(1);
    await expect(app.page.locator(".ask-turn").first()).toContainText(
      "First synthetic answer about the invoice.",
    );

    await app.page.fill("#ask-input", "What is the next action?");
    await app.page.press("#ask-input", "Enter");
    await expect(app.page.locator(".ask-turn")).toHaveCount(2);
    await expect(app.page.locator(".ask-turn").first()).toContainText(
      "First synthetic answer about the invoice.",
    );
    await expect(app.page.locator(".ask-turn").nth(1)).toContainText(
      "Second synthetic answer, using the same conversation.",
    );

    expect(requests).toHaveLength(2);
    expect(requests[0].conversation_id).toBeUndefined();
    expect(requests[1].conversation_id).toBe("conv-e2e");
    expect(requests[1].question).toBe("What is the next action?");
  });

  test("the thread header says whether the next message continues or starts a conversation", async ({
    app,
  }) => {
    const requests: AskRequest[] = [];
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route, request }) => {
          const body = request.postDataJSON() as AskRequest;
          requests.push(body);
          const turn = requests.length;
          await fulfillJson(
            route,
            askView({
              id: turn,
              question: body.question,
              conversation_id: "conv-destination",
              answer: "Synthetic destination answer " + turn + ".",
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.expectRoute("ask");
    const composer = app.page.locator(".ask-composer");
    const head = app.page.locator(".ask-thread-head");
    await expect(head).toHaveAttribute("data-ask-mode", "new");
    await expect(head).toContainText("New conversation");
    await expect(head).toContainText("Start a new conversation");
    await expect(head.getByRole("button", { name: "New conversation" })).toHaveCount(0);
    await expect(composer).toHaveAttribute("data-ask-mode", "new");
    await expect(composer.locator(".ask-destination")).toHaveText("Starting a new conversation");
    await expect(composer.locator(".ask-actions").getByRole("button", { name: "Send" })).toBeVisible();

    await app.page.fill("#ask-input", "Summarize the synthetic invoice thread");
    await app.page.press("#ask-input", "Enter");
    await expect(app.page.locator(".ask-turn")).toHaveCount(1);
    await expect(head).toHaveAttribute("data-ask-mode", "continue");
    await expect(head).toContainText("Current conversation");
    await expect(head).toContainText("Summarize the synthetic invoice thread");
    await expect(head).toContainText("1 exchange");
    await expect(composer.locator(".ask-destination")).toHaveText("Replying in this conversation");
    await expect(composer.locator(".ask-actions").getByRole("button", { name: "Reply" })).toBeVisible();

    const draft = "What should I do about the invoice?";
    await app.page.fill("#ask-input", draft);
    await head.getByRole("button", { name: "New conversation" }).click();
    await expect(app.page.locator(".ask-turn")).toHaveCount(0);
    await expect(head).toHaveAttribute("data-ask-mode", "new");
    await expect(app.page.locator("#ask-input")).toHaveValue(draft);
    await expect(composer.locator(".ask-actions").getByRole("button", { name: "Send" })).toBeVisible();

    await app.page.press("#ask-input", "Enter");
    await expect(app.page.locator(".ask-turn")).toHaveCount(1);
    await expect.poll(() => requests.length).toBe(2);
    expect(requests[1].conversation_id).toBeUndefined();
    expect(requests[1].question).toBe(draft);
    await expect(head).toHaveAttribute("data-ask-mode", "continue");
    await expect(head).toContainText("1 exchange");

    await app.page.locator("#ask-input").press("Alt+n");
    await expect(head).toHaveAttribute("data-ask-mode", "new");
    await expect(app.page.locator(".ask-turn")).toHaveCount(0);
  });

  test("a saved answer opens with a new-conversation destination", async ({ app }) => {
    const history = askView({
      id: 7,
      question: "Earlier synthetic question",
      conversation_id: "conv-old",
      answer: "Earlier synthetic answer.",
      created_at: "2026-10-20T09:00:00Z",
    });
    const row = {
      id: history.id,
      question: history.question,
      mode: history.mode,
      created_at: history.created_at,
    };
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [row] } },
      { method: "GET", path: "/api/ask/history/7", body: history },
    ]);

    await app.goto("/ask?id=7");
    await expect(app.page.locator(".ask-turn")).toHaveCount(1);
    const composer = app.page.locator(".ask-composer");
    const head = app.page.locator(".ask-thread-head");
    await expect(head).toHaveAttribute("data-ask-mode", "saved");
    await expect(head).toContainText("Earlier answer");
    await expect(head).toContainText("Earlier synthetic question");
    await expect(composer).toHaveAttribute("data-ask-mode", "saved");
    await expect(composer.locator(".ask-destination")).toContainText("starts a new conversation");
    await expect(composer.locator(".ask-actions").getByRole("button", { name: "Send" })).toBeVisible();
  });

  test("the model selector defaults to Auto and sends the chosen route", async ({ app }) => {
    const requests: AskRequest[] = [];
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "GET",
        path: "/api/providers",
        body: {
          providers: [{ id: "provider.example.test", label: "provider.example.test" }],
          roles: [{ setting: "qa.provider", kind: "generator", provider: "provider.example.test", configured: true, available: true }],
        },
      },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route, request }) => {
          const body = request.postDataJSON() as AskRequest;
          requests.push(body);
          await fulfillJson(
            route,
            askView({ id: requests.length, question: body.question, route: body.route }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    const select = app.page.locator(".ask-route-select");
    await expect(select).toHaveValue("auto");
    await expect(select.locator('option[value="remote"]')).toBeEnabled();
    await expect(app.page.locator(".ask-route-hint")).toContainText("your provider");

    await app.page.fill("#ask-input", "Summarize my day");
    await app.page.press("#ask-input", "Enter");
    await expect.poll(() => requests.length).toBe(1);
    expect(requests[0].route).toBe("auto");

    await select.selectOption("local");
    await expect(app.page.locator(".ask-route-hint")).toContainText("never calls a model");
    await app.page.fill("#ask-input", "What can local answer?");
    await app.page.press("#ask-input", "Enter");
    await expect.poll(() => requests.length).toBe(2);
    expect(requests[1].route).toBe("local");

    await select.selectOption("remote");
    await expect(app.page.locator(".ask-route-hint")).toContainText("provider.example.test");
    await app.page.fill("#ask-input", "What can remote answer?");
    await app.page.press("#ask-input", "Enter");
    await expect.poll(() => requests.length).toBe(3);
    expect(requests[2].route).toBe("remote");
  });

  test("clarify options are one-click follow-ups and stay visible above the answer", async ({
    app,
  }) => {
    const requests: AskRequest[] = [];
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route, request }) => {
          const body = request.postDataJSON() as AskRequest;
          requests.push(body);
          if (requests.length === 1) {
            await fulfillJson(route, clarifyView());
            return;
          }
          await fulfillJson(
            route,
            askView({
              id: 2,
              question: body.question,
              conversation_id: "conv-e2e",
              answer: "The synthetic invoice is due Friday.",
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "Summarize the last exchange with Nadia");
    await app.page.press("#ask-input", "Enter");

    const options = app.page.locator(".ask-clarify-options button");
    await expect(options).toHaveCount(2);
    await expect(
      app.page.locator(".ask-clarify-options button.ask-clarify-card"),
    ).toHaveCount(2);
    const first = options.first();
    await expect(first.locator(".ask-clarify-card-name")).toHaveText(
      "Nadia Tremblay",
    );
    await expect(first.locator(".ask-clarify-card-address")).toHaveText(
      "nadia@atelier-nord.test",
    );
    await expect(first.locator(".ask-clarify-card-count")).toHaveText(
      "10 messages",
    );
    await expect(first).toHaveAccessibleName(/Nadia Tremblay.*10 messages/);
    await expect(first).not.toHaveAccessibleName(/Summarize the last exchange/);
    const followUp = await first.getAttribute("data-question");
    expect(followUp).toBe(
      "Summarize the last exchange with Nadia Tremblay — Nadia Tremblay <nadia@atelier-nord.test>",
    );
    expect((await first.innerText()).trim()).not.toBe(followUp);
    await first.click();

    await expect(app.page.locator(".ask-turn")).toHaveCount(2);
    await expect(app.page.locator(".ask-clarify")).toBeVisible();
    await expect(app.page.locator(".ask-turn").nth(1)).toContainText(
      "The synthetic invoice is due Friday.",
    );
    expect(requests[1].question).toBe(followUp);
    expect(requests[1].conversation_id).toBe("conv-e2e");
  });

  test("an agenda answer renders the projected rows and counts", async ({
    app,
  }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route }) => {
          await fulfillJson(
            route,
            askView({
              id: 13,
              mode: "computed",
              intent: "agenda",
              answer: "",
              agenda: agendaView({ truncated: true }),
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "What should I do next?");
    await app.page.press("#ask-input", "Enter");

    const list = app.page.locator(".ask-agenda-list");
    await expect(list.locator(".agenda-row")).toHaveCount(3);
    await expect(app.page.locator(".ask-computed .ask-sentence")).toContainText(
      "1 events · 1 replies owed · 1 due",
    );
    await expect(app.page.locator(".ask-computed .ask-sentence")).toContainText(
      "2 more waiting",
    );
    await expect(list).toContainText("Synthetic review");
    await expect(list).toContainText("Before Synthetic review");
    await expect(list.locator(".agenda-row-link")).toHaveCount(2);
    await expect(app.page.locator(".ask-agenda-list + .agenda-truncated")).toHaveText(
      "The list is cut short; the counts describe the whole result.",
    );
  });

  test("a topic contact answer ranks people without activity columns", async ({
    app,
  }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route }) => {
          await fulfillJson(route, topicContactsView());
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "Who mentioned AI?");
    await app.page.press("#ask-input", "Enter");

    const table = app.page.locator(".ask-contact-table");
    await expect(table).toHaveCount(1);
    await expect(table).toContainText("Nadia Tremblay");
    await expect(table).toContainText("nadia@atelier-nord.test");
    await expect(table).toContainText("Nadia Smith");
    await expect(table).toContainText("nadia.smith@beta.test");

    const lead = app.page.locator(".ask-computed .ask-sentence");
    await expect(lead).toContainText("IA intelligence artificielle");
    await expect(lead).toContainText("2 contacts · 9 matching messages");
    await expect(app.page.locator(".ask-clarify")).toHaveCount(0);

    const headers = table.locator("thead th");
    await expect(headers).toHaveCount(4);
    await expect(table.locator("thead")).not.toContainText("Sent");
    await expect(table.locator("thead")).not.toContainText("Threads");
  });

  test("the topic answer never scrolls sideways at any viewport", async ({
    app,
  }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route }) => {
          await fulfillJson(route, topicContactsView());
        },
      },
    ]);
    await app.goto("/ask");
    await app.page.fill("#ask-input", "Who mentioned AI?");
    await app.page.press("#ask-input", "Enter");
    const table = app.page.locator(".ask-contact-table");
    await expect(table).toHaveCount(1);

    for (const width of [320, 360, 420, 768, 1024, 1440]) {
      await app.page.setViewportSize({ width, height: 900 });
      const probe = await app.page.evaluate(() => {
        const doc = document.documentElement;
        const offenders: string[] = [];
        document.querySelectorAll("*").forEach((element) => {
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
        "document overflows at " + width + "px",
      ).toBeLessThanOrEqual(1);
      expect(
        probe.offenders,
        "horizontal scrollers at " + width + "px",
      ).toEqual([]);
      await expect(table).toContainText("Nadia Tremblay");
      await expect(table).toContainText("nadia@atelier-nord.test");
    }
  });

  test("the pending turn is inline and the composer stays usable", async ({ app }) => {
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route, request }) => {
          await gate;
          const body = request.postDataJSON() as AskRequest;
          await fulfillJson(
            route,
            askView({ id: 9, question: body.question, answer: "Released answer." }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "A question that waits");
    await app.page.press("#ask-input", "Enter");

    await expect(app.page.locator(".ask-turn-pending")).toHaveCount(1);
    await expect(app.page.locator(".thinking-card")).toBeVisible();
    await expect(app.page.locator("#ask-input")).toBeDisabled();

    release?.();
    await expect(app.page.locator(".ask-turn-pending")).toHaveCount(0);
    await expect(app.page.locator(".ask-turn")).toHaveCount(1);
    await expect(app.page.locator(".ask-turn")).toContainText("Released answer.");
    await expect(app.page.locator("#ask-input")).toBeEnabled();
  });

  test("a provider-less structured count renders its sentence and citation", async ({
    app,
  }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route, request }) => {
          const body = request.postDataJSON() as AskRequest;
          await fulfillJson(
            route,
            askView({
              id: 11,
              question: body.question,
              answer: "",
              mode: "structured",
              intent: "deadline",
              counts: { deadline: 1, total: 8 },
              citations: [
                {
                  message_id: 1,
                  subject: "Synthetic deadline",
                  sender_name: "Demo Sender",
                  received_at: "2026-10-20T10:00:00Z",
                  reason: "deadline",
                },
              ],
              provider: "",
              model: "",
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "How many deadlines do I have?");
    await app.page.press("#ask-input", "Enter");
    await expect(app.page.locator(".ask-sentence")).toContainText(
      "1 message has a deadline.",
    );
    await expect(app.page.locator(".ask-citations")).toContainText(
      "Synthetic deadline",
    );
    await expect(app.page.locator(".ask-turn").first()).toContainText(
      "How many deadlines do I have?",
    );
  });

  // The count is structured: Go ships the catalogue path and the coverage
  // numbers, and the UI owns the display copy. The sentence composes the
  // labels.option.topic and subject.<topic>.<subtopic> entries; the coverage
  // line repeats ask.topic.coverage behind the count.
  test("a topic count names its catalogue path and coverage", async ({ app }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route, request }) => {
          const body = request.postDataJSON() as AskRequest;
          await fulfillJson(
            route,
            askView({
              id: 12,
              question: body.question,
              answer: "",
              mode: "structured",
              intent: "topic",
              topic: {
                topic: "billing_account",
                subtopic: "invoice",
                count: 3,
                classified: 120,
                scanned: 200,
              },
              citations: [],
              provider: "",
              model: "",
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "How many invoices are there?");
    await app.page.press("#ask-input", "Enter");

    const sentence = app.page.locator(".ask-sentence");
    await expect(sentence).toHaveCount(1);
    await expect(sentence).toHaveText(
      "3 messages are classified as “Billing or account › Invoice”.",
    );
    await expect(app.page.locator(".ask-topic-coverage")).toHaveText(
      "120 of 200 messages have a topic.",
    );
  });

  // The 0/0 census is disclosed, never hidden: the coverage line says the
  // count was read from a mailbox whose mail was never scanned, so an empty
  // topic cannot look like an empty mailbox. Assert the visible wording, not
  // the catalogue key that owns it.
  test("a zero topic count discloses that nothing was scanned", async ({
    app,
  }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route }) => {
          await fulfillJson(
            route,
            askView({
              id: 14,
              question: "How many invoices are there?",
              answer: "",
              mode: "structured",
              intent: "topic",
              topic: {
                topic: "billing_account",
                subtopic: "invoice",
                count: 0,
                classified: 0,
                scanned: 0,
              },
              citations: [],
              provider: "",
              model: "",
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "How many invoices are there?");
    await app.page.press("#ask-input", "Enter");

    await expect(app.page.locator(".ask-sentence")).toHaveText(
      "No message is classified as “Billing or account › Invoice”.",
    );
    await expect(app.page.locator(".ask-topic-coverage")).toHaveText(
      "No message was scanned yet, so this count is not certified.",
    );
  });

  test("a no-match notice is a notice, not a fabricated answer", async ({ app }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route }) => {
          await fulfillJson(
            route,
            askView({
              id: 4,
              mode: "generated",
              answer: "",
              notice: "no_matches",
              citations: [],
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "What did the moon say?");
    await app.page.press("#ask-input", "Enter");
    await expect(app.page.locator(".ask-nomatch")).toBeVisible();
    await expect(app.page.locator(".ask-nomatch")).toContainText(
      "No matching mail",
    );
    await expect(app.page.locator(".ask-citations")).toHaveCount(0);
  });

  test("new conversation clears the transcript and starts a fresh conversation", async ({
    app,
  }) => {
    const requests: AskRequest[] = [];
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
      {
        method: "POST",
        path: "/api/ask",
        handler: async ({ route, request }) => {
          const body = request.postDataJSON() as AskRequest;
          requests.push(body);
          await fulfillJson(
            route,
            askView({
              id: requests.length,
              question: body.question,
              conversation_id: "conv-" + requests.length,
              answer: "Synthetic answer " + requests.length + ".",
            }),
          );
        },
      },
    ]);

    await app.goto("/ask");
    await app.page.fill("#ask-input", "First question");
    await app.page.press("#ask-input", "Enter");
    await app.page.fill("#ask-input", "Second question");
    await app.page.press("#ask-input", "Enter");
    await expect(app.page.locator(".ask-turn")).toHaveCount(2);

    await app.page.getByRole("button", { name: "New conversation" }).click();
    await expect(app.page.locator(".ask-turn")).toHaveCount(0);
    await expect(app.page.locator(".ask-empty-suggestions")).toBeVisible();

    await app.page.fill("#ask-input", "Third question");
    await app.page.press("#ask-input", "Enter");
    await expect(app.page.locator(".ask-turn")).toHaveCount(1);
    // The pending turn paints before the POST reaches the mock; wait for the
    // request itself before asserting on its payload.
    await expect.poll(() => requests.length).toBe(3);
    expect(requests[2].conversation_id).toBeUndefined();
  });

  test("opening a history answer marks it as an earlier answer", async ({ app }) => {
    const history = askView({
      id: 7,
      question: "Earlier synthetic question",
      conversation_id: "conv-old",
      answer: "Earlier synthetic answer.",
      created_at: "2026-10-20T09:00:00Z",
    });
    // The rail receives a projection; the answer itself is the detail read.
    const row = {
      id: history.id,
      question: history.question,
      mode: history.mode,
      created_at: history.created_at,
    };
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [row] } },
      { method: "GET", path: "/api/ask/history/7", body: history },
    ]);

    await app.goto("/ask?id=7");
    await expect(app.page.locator(".ask-turn")).toHaveCount(1);
    await expect(app.page.locator(".ask-turn")).toContainText(
      "Earlier synthetic answer.",
    );
    await expect(app.page.locator(".ask-past-note")).toBeVisible();
  });

  test("a too-short question marks the composer invalid and clears on input", async ({
    app,
  }) => {
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [] } },
    ]);
    await app.goto("/ask");
    await app.expectRoute("ask");

    const input = app.page.locator("#ask-input");
    await input.fill("ab");
    await input.press("Enter");
    await expect(input).toHaveAttribute("aria-invalid", "true");
    const describedBy = await input.getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();
    await expect(app.page.locator("#" + describedBy)).toHaveText(
      "Write at least a few words.",
    );

    await input.fill("A real question");
    await expect(input).not.toHaveAttribute("aria-invalid", "true");
  });

  test("double-clicking a dialog confirmation sends one request", async ({ app }) => {
    const deletes: string[] = [];
    const history = askView({
      id: 7,
      question: "Earlier synthetic question",
      conversation_id: "conv-old",
      answer: "Earlier synthetic answer.",
      created_at: "2026-10-20T09:00:00Z",
    });
    const row = {
      id: history.id,
      question: history.question,
      mode: history.mode,
      created_at: history.created_at,
    };
    await mockApi(app.page, [
      { method: "GET", path: "/api/ask/history", body: { items: [row] } },
      { method: "GET", path: "/api/ask/history/7", body: history },
      {
        method: "DELETE",
        path: "/api/ask/history",
        handler: async ({ route }) => {
          deletes.push("DELETE");
          await route.fulfill({ status: 204 });
        },
      },
    ]);

    await app.goto("/ask");
    await app.expectRoute("ask");
    await app.page.getByRole("button", { name: "Clear history" }).click();
    const confirm = app.page
      .locator("#modal")
      .getByRole("button", { name: "Clear history" });
    await confirm.evaluate((node) => {
      node.click();
      node.click();
    });
    await expect(app.page.locator(".ask-history-list")).toContainText(
      "No questions yet.",
    );
    expect(deletes).toHaveLength(1);
  });
});
