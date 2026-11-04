import { expect, test } from "../fixtures/test";

// The browser specs mock content endpoints. These probes keep the mocked
// envelopes honest against the real server: the fixture builders must carry
// the fields the handlers actually emit. They use the empty/preview database
// only, so no provider call and no personal data are involved.
test.describe("fixture contract", () => {
  test("content envelopes still carry the fields the UI fixtures use", async ({
    app,
  }) => {
    const auth = { Authorization: `Bearer ${app.server.token}` };
    const get = async (path: string) => {
      const response = await app.page.request.get(app.server.baseURL + path, {
        headers: auth,
      });
      expect(response.ok()).toBeTruthy();
      return response.json();
    };

    const learning = await get("api/learning");
    expect(learning).toMatchObject({
      open: expect.any(Boolean),
      preview: expect.any(Boolean),
      status: expect.objectContaining({
        state: expect.any(String),
        stages: expect.any(Array),
      }),
    });

    const companies = await get("api/companies?limit=25");
    expect(companies).toMatchObject({
      generated_at: expect.any(String),
      companies: expect.any(Array),
      coverage: expect.objectContaining({
        contacts: expect.any(Number),
        contacts_with_company: expect.any(Number),
      }),
    });

    const priority = await get("api/priority?limit=5&filter=all");
    expect(priority).toMatchObject({
      items: expect.any(Array),
      total: expect.any(Number),
      available: expect.any(Number),
      offset: expect.any(Number),
      has_more: expect.any(Boolean),
      generated_at: expect.any(String),
      preview: expect.any(Boolean),
    });

    const logs = await get("api/logs?limit=5");
    expect(logs).toHaveProperty("entries");
    expect(Array.isArray(logs.entries)).toBe(true);

    const askResponse = await app.page.request.post(
      app.server.baseURL + "api/ask",
      {
        headers: { ...auth, "Content-Type": "application/json" },
        data: { question: "Synthetic contract probe", language: "en" },
      },
    );
    expect(askResponse.ok()).toBeTruthy();
    const ask = await askResponse.json();
    for (const field of [
      "id",
      "conversation_id",
      "question",
      "mode",
      "intent",
      "counts",
      "citations",
      "provider",
      "model",
      "took_ms",
      "preview",
      "created_at",
    ]) {
      expect(ask).toHaveProperty(field);
    }
  });
});
