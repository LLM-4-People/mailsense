import { fulfillJson, mockApi } from "../fixtures/mock";
import { expect, test } from "../fixtures/test";

type SidecarView = {
  builds: Array<Record<string, unknown>>;
  runtimes: Array<Record<string, unknown>>;
};

function sidecarView(unavailable: string): SidecarView {
  return {
    builds: [
      {
        runtime: "llama-server",
        version: "b11617",
        variant: "vulkan",
        gpu: true,
        auto: true,
        installed: false,
        size_bytes: 0,
        download_bytes: 342_039_075,
        pins: [],
        unavailable,
      },
    ],
    runtimes: [],
  };
}

test("an unavailable Vulkan build hides repair and first-use copy, and Refresh re-probes", async ({
  app,
}) => {
  let unavailable = "vulkan";
  let sidecarReads = 0;
  await mockApi(app.page, [
    {
      method: "GET",
      path: "/api/settings",
      body: {
        settings: [
          {
            key: "embed.provider",
            type: "string",
            default: "",
            pattern: "^$|^[a-z0-9][a-z0-9_-]*$",
            group: "Embeddings",
            description: "Record id of the embedder used by the semantic index and search.",
            value: "emb-local",
            layer: "file",
            restart_pending: false,
          },
          {
            key: "sidecar.threads",
            type: "int",
            default: 0,
            min: 0,
            max: 1024,
            group: "Local runtime",
            description: "Threads of the local runtime; 0 measures once at the first start.",
            value: 8,
            layer: "file",
            restart_pending: false,
          },
          {
            key: "sidecar.idle_timeout",
            type: "duration",
            default: "15m",
            min: "0s",
            max: "24h",
            group: "Local runtime",
            description: "The runtime is stopped after this long without a request; 0 keeps it running.",
            value: "15m0s",
            layer: "default",
            restart_pending: false,
          },
        ],
        restart_pending: [],
      },
    },
    {
      method: "GET",
      path: "/api/providers",
      body: {
        providers: [
          {
            id: "emb-local",
            label: "Local embedder",
            kind: "embedder",
            protocol: "openai_embeddings",
            model: "embeddinggemma-2",
            runtime: "llama-server",
          },
        ],
        roles: [],
      },
    },
    {
      method: "GET",
      path: "/api/sidecar",
      handler: async ({ route }) => {
        sidecarReads += 1;
        await fulfillJson(route, sidecarView(unavailable));
      },
    },
  ]);

  // Go to Settings, then the Local runtime tab (the runtime card is not
  // mounted on the general tab).
  await app.goto("/settings");
  await app.page.getByRole("button", { name: "Local runtime", exact: true }).click();

  const card = app.page.locator(".runtime-card");
  const runtimeSection = app.page.locator(".section").filter({ has: card });
  let documentLoads = 0;
  app.page.on("load", () => {
    documentLoads += 1;
  });
  await expect(app.page.getByRole("heading", { name: "Local runtime" })).toBeVisible();
  // The tab owns the engine's settings: status and controls in one place,
  // never a second copy in General.
  await expect(app.page.getByRole("heading", { name: "Engine settings" })).toBeVisible();
  await expect(app.page.locator(".setting-row").filter({ hasText: "sidecar.threads" })).toBeVisible();
  await expect(card).toBeVisible();
  await expect.poll(() => sidecarReads).toBeGreaterThanOrEqual(1);

  // Unavailable Vulkan: the cause, the unavailable pill, and no offer that
  // would download the 342 MB install.
  await expect(card.locator(".pill")).toHaveText("Unavailable");
  await expect(card.locator(".runtime-note.warn-text")).toContainText(
    "No usable Vulkan device was found",
  );
  // A stopped engine must name its idle window: the card reads the same
  // sidecar.idle_timeout the settings rows below write.
  await expect(card).toContainText(
    "It stops after 15 min without a request and starts again on the next use.",
  );
  await expect(card.getByText("It downloads and verifies automatically on first use")).toHaveCount(0);
  await expect(card.getByText("342 MB")).toHaveCount(0);
  // One action in the section head (Refresh): the manual build UI is gone.
  // The settings rows below carry their own Apply/Reset, so the count is
  // scoped to the head, not the whole section.
  await expect(runtimeSection.locator(".section-head").getByRole("button")).toHaveCount(1);
  await expect(runtimeSection.getByRole("button", { name: "Refresh" })).toBeVisible();
  await expect(card.locator("button", { hasText: "Reinstall / Repair" })).toHaveCount(0);

  // The shell can re-base live reads once shortly after boot (a dropped
  // event resync). Let it land first: the next status read then belongs to
  // Refresh, and the 15 s cache is fresh either way.
  let lastReads = sidecarReads;
  let quietSince = Date.now();
  const settleDeadline = Date.now() + 10_000;
  while (Date.now() < settleDeadline) {
    await app.page.waitForTimeout(250);
    if (sidecarReads !== lastReads) {
      lastReads = sidecarReads;
      quietSince = Date.now();
    } else if (Date.now() - quietSince >= 1_000) {
      break;
    }
  }

  // The device appears: Refresh must invalidate the 15 s status cache and
  // issue a new /api/sidecar read, then the card must show the normal
  // first-use/install offer without a page reload.
  const beforeRefresh = sidecarReads;
  unavailable = "";
  const reread = app.page.waitForRequest(
    (request) =>
      request.method() === "GET" && new URL(request.url()).pathname === "/api/sidecar",
    { timeout: 2_000 },
  );
  await runtimeSection.getByRole("button", { name: "Refresh" }).click();
  await reread;
  await expect.poll(() => sidecarReads, { timeout: 2_000 }).toBe(beforeRefresh + 1);

  await expect(card.locator(".pill")).toHaveText("Not installed");
  await expect(card.locator(".runtime-note.warn-text")).toHaveCount(0);
  await expect(card.getByText("It downloads and verifies automatically on first use")).toBeVisible();
  await expect(card.getByText("342 MB")).toBeVisible();
  await expect(card).toContainText(
    "It stops after 15 min without a request and starts again on the next use.",
  );
  await expect(runtimeSection.getByRole("button", { name: "Refresh" })).toBeVisible();
  expect(documentLoads).toBe(0);
  await card.locator("details.runtime-advanced summary").click();
  await expect(card.getByRole("button", { name: "Reinstall / Repair" })).toBeVisible();
});
