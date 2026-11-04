// Cold HTTP cache benchmark for the Home route. One real Mailsense binary is
// built and started against a fresh temporary data directory, then one fresh
// Chromium process opens a fresh context. The browser cache is explicitly
// cleared through CDP before the first navigation. Cold load and warm reload
// are measured in the same process from the page's navigation timeline and
// from CDP Network events.
//
//   node e2e/bench-cold-load.mjs
//
// The cold run asserts that Network.requestServedFromCache never fired. The
// server sends Cache-Control: no-store for the document and no-cache for the
// shell assets, so a warm reload is expected to revalidate rather than serve
// an unvalidated cache hit; the report contains whatever was measured.
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const binary = path.join(
  root,
  "e2e",
  ".cache",
  process.platform === "win32" ? "mailsense.exe" : "mailsense",
);
const outFile = path.join(root, "out", "bench", "cold-load.json");

// The final Home screen removes aria-busy from the route skeleton; the brief
// section is the real content node rendered by renderHub.
const HOME_SELECTOR =
  '#main .home-screen:not([aria-busy="true"]) [data-scroll-key="home-brief"]';
const ASSET_PATHS = ["/app.js", "/app.css", "/mail.css", "/api/boot", "/api/status"];
const READY_TIMEOUT_MS = 30_000;
const NAV_TIMEOUT_MS = 30_000;
const BUILD_TIMEOUT_MS = 180_000;
const SHUTDOWN_TIMEOUT_MS = 10_000;
const DRAIN_MS = 200;
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function round(value) {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.round(value * 10) / 10
    : null;
}

function build() {
  mkdirSync(path.dirname(binary), { recursive: true });
  try {
    execFileSync("go", ["build", "-o", binary, "./cmd/mailsense"], {
      cwd: root,
      timeout: BUILD_TIMEOUT_MS,
      stdio: ["ignore", "pipe", "pipe"],
      encoding: "utf8",
    });
  } catch (error) {
    process.stderr.write(String(error?.stdout || ""));
    process.stderr.write(String(error?.stderr || ""));
    throw new Error("go build failed");
  }
}

async function waitForDescriptor(dataDir) {
  const file = path.join(dataDir, "runtime", "api.json");
  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    try {
      const parsed = JSON.parse(readFileSync(file, "utf8"));
      if (parsed.url && parsed.token) return parsed;
    } catch {
      // The descriptor is written atomically after the listener is up.
    }
    await sleep(25);
  }
  throw new Error(`Mailsense did not publish ${file} within ${READY_TIMEOUT_MS}ms`);
}

async function waitForStatus(baseURL, token) {
  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(baseURL + "api/status", {
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(5_000),
      });
      if (response.ok) return;
    } catch {
      // Not listening yet.
    }
    await sleep(100);
  }
  throw new Error("Mailsense API did not become ready");
}

// A new server publishes its descriptor as soon as the listener accepts,
// before the derived warm. Wait for the boot contract to report ready, with
// the same 404 fallback as e2e/fixtures/server.ts.
async function waitForBoot(baseURL, token) {
  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(baseURL + "api/boot", {
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(5_000),
      });
      if (response.status === 404) {
        await waitForStatus(baseURL, token);
        return;
      }
      if (response.ok) {
        const view = await response.json();
        if (view.state === "ready") return;
      }
    } catch {
      // Not listening yet, or the warm is still running.
    }
    await sleep(100);
  }
  throw new Error("Mailsense API did not report boot ready");
}

async function readStatus(baseURL, token) {
  const response = await fetch(baseURL + "api/status", {
    headers: { Authorization: `Bearer ${token}` },
    signal: AbortSignal.timeout(30_000),
  });
  if (!response.ok) throw new Error(`GET /api/status returned ${response.status}`);
  return response.json();
}

async function waitForExit(proc, timeout) {
  if (proc.exitCode !== null) return;
  await new Promise((resolve) => {
    const timer = setTimeout(resolve, timeout);
    proc.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

async function stopServer(server) {
  if (!server) return;
  const { proc, baseURL, token, dataDir, sidecarDir } = server;
  if (proc.exitCode === null) {
    try {
      await fetch(baseURL + "api/shutdown", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
        body: "{}",
        signal: AbortSignal.timeout(5_000),
      });
    } catch {
      // The process may already be gone.
    }
  }
  await waitForExit(proc, SHUTDOWN_TIMEOUT_MS);
  if (proc.exitCode === null) proc.kill("SIGTERM");
  await waitForExit(proc, 3_000);
  if (proc.exitCode === null) proc.kill("SIGKILL");
  await waitForExit(proc, 2_000);
  await rm(dataDir, { recursive: true, force: true });
  await rm(sidecarDir, { recursive: true, force: true });
}

// The binary binds 127.0.0.1:0 itself and publishes the chosen free port in
// runtime/api.json, so no port is guessed here.
async function startServer(dataDir, sidecarDir) {
  const env = {
    ...process.env,
    MAILSENSE_DATA_DIR: dataDir,
    MAILSENSE_LEARNING_TEST_MODE: "true",
    MAILSENSE_UI_LANGUAGE: "en",
    MAILSENSE_SIDECAR_DIR: sidecarDir,
    MAILSENSE_SIDECAR_AUTO_INSTALL: "false",
    MAILSENSE_EVIDENCE_AFTER_RUNS: "false",
    MAILSENSE_LAYER1_AFTER_RUNS: "false",
    TZ: "UTC",
  };
  // The desktop opener must never fire during a benchmark run.
  delete env.DISPLAY;
  delete env.WAYLAND_DISPLAY;

  let output = "";
  const proc = spawn(binary, [], {
    env,
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true,
  });
  proc.stdout?.on("data", (chunk) => {
    output = (output + String(chunk)).slice(-6000);
  });
  proc.stderr?.on("data", (chunk) => {
    output = (output + String(chunk)).slice(-6000);
  });

  const server = { proc, baseURL: "", token: "", dataDir, sidecarDir, output: () => output };
  try {
    const descriptor = await waitForDescriptor(dataDir);
    server.baseURL = descriptor.url;
    server.token = descriptor.token;
    await waitForBoot(descriptor.url, descriptor.token);
    return server;
  } catch (error) {
    await stopServer(server);
    throw new Error(
      `${error instanceof Error ? error.message : String(error)}\n${output}`,
    );
  }
}

function newCapture() {
  return { requests: new Map(), order: [], cacheHits: 0 };
}

function ensureRequest(capture, requestId) {
  let entry = capture.requests.get(requestId);
  if (!entry) {
    entry = {
      requestId,
      url: "",
      path: "",
      method: "",
      type: "",
      status: null,
      mimeType: "",
      cacheControl: "",
      ifNoneMatch: false,
      servedFromCache: false,
      fromDiskCache: false,
      fromServiceWorker: false,
      fromPrefetchCache: false,
      bytes: 0,
      responseBytes: 0,
      failed: false,
      errorText: "",
    };
    capture.requests.set(requestId, entry);
    capture.order.push(requestId);
  }
  return entry;
}

function readPath(url) {
  try {
    return new URL(url).pathname;
  } catch {
    return "";
  }
}

function header(headers, name) {
  if (!headers || typeof headers !== "object") return "";
  const wanted = name.toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() === wanted) return String(value);
  }
  return "";
}

function summarize(capture) {
  const all = capture.order
    .map((id) => capture.requests.get(id))
    .filter((entry) => entry && entry.url && !entry.url.startsWith("data:"));

  const assets = {};
  for (const assetPath of ASSET_PATHS) {
    const hits = all.filter((entry) => entry.path === assetPath);
    assets[assetPath] = {
      requests: hits.length,
      statuses: [...new Set(hits.map((entry) => entry.status).filter((s) => s !== null))],
      bytes: hits.reduce((sum, entry) => sum + (Number(entry.bytes) || 0), 0),
      cache_hits: hits.filter((entry) => entry.servedFromCache).length,
      from_disk_cache: hits.filter((entry) => entry.fromDiskCache).length,
      conditional_headers_observed: hits.filter((entry) => entry.ifNoneMatch).length,
      response_bytes: hits.reduce((sum, entry) => sum + (Number(entry.responseBytes) || 0), 0),
      cache_control: hits.map((entry) => entry.cacheControl).filter(Boolean)[0] || "",
    };
  }

  return {
    requests: all.length,
    bytes: all.reduce((sum, entry) => sum + (Number(entry.bytes) || 0), 0),
    cacheHits: capture.cacheHits,
    servedFromCache: all.filter((entry) => entry.servedFromCache).length,
    fromDiskCache: all.filter((entry) => entry.fromDiskCache).length,
    failed: all.filter((entry) => entry.failed).length,
    assets,
  };
}

async function readTiming(page, selector) {
  return page.evaluate(
    ({ selector, wanted }) => {
      const nav = performance.getEntriesByType("navigation")[0];
      const bench = window.__coldBench || {};
      const resources = performance
        .getEntriesByType("resource")
        .map((entry) => {
          let pathname = "";
          try {
            pathname = new URL(entry.name).pathname;
          } catch {
            // Ignore unparsable names.
          }
          return {
            path: pathname,
            transferSize: entry.transferSize,
            encodedBodySize: entry.encodedBodySize,
            decodedBodySize: entry.decodedBodySize,
            duration: entry.duration,
            initiatorType: entry.initiatorType,
            responseStatus: entry.responseStatus ?? null,
          };
        })
        .filter((entry) => wanted.includes(entry.path));
      return {
        mounted: !!document.querySelector(selector),
        screen: document.documentElement.dataset.screen || "",
        url: location.href,
        routeAt: typeof bench.routeAt === "number" ? bench.routeAt : null,
        nav: nav
          ? {
              responseStart: nav.responseStart,
              domContentLoadedEventEnd: nav.domContentLoadedEventEnd,
              loadEventEnd: nav.loadEventEnd,
              duration: nav.duration,
              transferSize: nav.transferSize,
              encodedBodySize: nav.encodedBodySize,
            }
          : null,
        resources,
      };
    },
    { selector, wanted: ASSET_PATHS },
  );
}

function assertCold(net) {
  const problems = [];
  if (net.cacheHits !== 0) {
    problems.push(`Network.requestServedFromCache fired ${net.cacheHits} time(s)`);
  }
  if (net.servedFromCache !== 0) {
    problems.push(`${net.servedFromCache} request(s) were served from cache`);
  }
  if (net.fromDiskCache !== 0) {
    problems.push(`${net.fromDiskCache} response(s) came from the disk cache`);
  }
  const js = net.assets["/app.js"];
  if (!js || js.requests === 0) {
    problems.push("/app.js was not requested");
  } else {
    if (js.cache_hits !== 0) problems.push("/app.js was served from cache");
    if (!js.statuses.includes(200)) {
      problems.push(`/app.js status was ${JSON.stringify(js.statuses)}, not 200`);
    }
  }
  if (problems.length) {
    throw new Error(`cold cache proof failed: ${problems.join("; ")}`);
  }
}

async function measure(page, selector, action) {
  const capture = newCapture();
  begin(capture);
  try {
    await action();
    await page.locator(selector).first().waitFor({ state: "visible", timeout: NAV_TIMEOUT_MS });
    // Let the final append paint before the route timestamp fallback.
    await page.evaluate(
      () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
    );
    await sleep(DRAIN_MS);
    const timing = await readTiming(page, selector);
    if (timing.routeAt === null) {
      timing.routeAt = await page.evaluate(() => performance.now());
      timing.routeSource = "fallback";
    } else {
      timing.routeSource = "observer";
    }
    return { timing, net: end() };
  } catch (error) {
    end();
    throw error;
  }
}

// ---------------------------------------------------------------- CDP capture

let activeCapture = null;

function begin(capture) {
  activeCapture = capture;
}

function end() {
  const capture = activeCapture;
  activeCapture = null;
  return capture ? summarize(capture) : null;
}

function attachNetwork(cdp) {
  cdp.on("Network.requestWillBeSent", (params) => {
    if (!activeCapture) return;
    const entry = ensureRequest(activeCapture, params.requestId);
    entry.url = params.request?.url || "";
    entry.path = readPath(entry.url);
    entry.method = params.request?.method || "";
    entry.type = params.type || "";
    if (header(params.request?.headers, "if-none-match")) entry.ifNoneMatch = true;
  });

  // requestWillBeSent may omit conditional headers; the extra-info event
  // carries the headers Chromium actually sent on the wire.
  cdp.on("Network.requestWillBeSentExtraInfo", (params) => {
    if (!activeCapture) return;
    const entry = ensureRequest(activeCapture, params.requestId);
    if (header(params.headers, "if-none-match")) entry.ifNoneMatch = true;
  });

  cdp.on("Network.requestServedFromCache", (params) => {
    if (!activeCapture) return;
    const entry = ensureRequest(activeCapture, params.requestId);
    entry.servedFromCache = true;
    activeCapture.cacheHits += 1;
  });

  cdp.on("Network.responseReceived", (params) => {
    if (!activeCapture) return;
    const entry = ensureRequest(activeCapture, params.requestId);
    const response = params.response || {};
    entry.status = typeof response.status === "number" ? response.status : null;
    entry.mimeType = response.mimeType || "";
    entry.cacheControl = header(response.headers, "cache-control");
    entry.fromDiskCache = !!response.fromDiskCache;
    entry.fromServiceWorker = !!response.fromServiceWorker;
    entry.fromPrefetchCache = !!response.fromPrefetchCache;
    entry.responseBytes = Number(response.encodedDataLength) || 0;
  });

  cdp.on("Network.loadingFinished", (params) => {
    if (!activeCapture) return;
    const entry = ensureRequest(activeCapture, params.requestId);
    entry.bytes += Number(params.encodedDataLength) || 0;
  });

  cdp.on("Network.loadingFailed", (params) => {
    if (!activeCapture) return;
    const entry = ensureRequest(activeCapture, params.requestId);
    entry.failed = true;
    entry.errorText = params.errorText || "";
  });
}

// ---------------------------------------------------------------- main

let server = null;
let browser = null;
let dataDir = null;
let sidecarDir = null;

try {
  build();
  dataDir = await mkdtemp(path.join(tmpdir(), "mailsense-cold-bench-"));
  sidecarDir = await mkdtemp(path.join(tmpdir(), "mailsense-cold-sidecar-"));
  server = await startServer(dataDir, sidecarDir);
  const status = await readStatus(server.baseURL, server.token);

  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    locale: "en-US",
    timezoneId: "UTC",
  });
  const page = await context.newPage();
  await page.addInitScript(
    ({ token, selector }) => {
      try {
        sessionStorage.setItem("mailsense.token", token);
      } catch {
        // Opaque origins cannot use storage.
      }
      window.__coldBench = { routeAt: null };
      const seen = () => {
        if (window.__coldBench.routeAt !== null) return;
        if (!document.querySelector(selector)) return;
        window.__coldBench.routeAt = performance.now();
      };
      const observer = new MutationObserver(seen);
      observer.observe(document, {
        childList: true,
        subtree: true,
        attributes: true,
        attributeFilter: ["aria-busy"],
      });
      addEventListener("DOMContentLoaded", seen, { once: true });
      addEventListener("load", seen, { once: true });
    },
    { token: server.token, selector: HOME_SELECTOR },
  );

  const cdp = await context.newCDPSession(page);
  await cdp.send("Network.enable");
  // Cache stays enabled so the warm reload is a real comparison; the fresh
  // context and the explicit clear make the cold navigation start empty.
  await cdp.send("Network.setCacheDisabled", { cacheDisabled: false });
  await cdp.send("Network.clearBrowserCache");
  attachNetwork(cdp);

  const cold = await measure(page, HOME_SELECTOR, () =>
    page.goto(server.baseURL, { waitUntil: "load", timeout: NAV_TIMEOUT_MS }),
  );
  assertCold(cold.net);

  // Drain CDP events from the cold document before starting the warm window.
  await sleep(300);
  const warm = await measure(page, HOME_SELECTOR, () =>
    page.reload({ waitUntil: "load", timeout: NAV_TIMEOUT_MS }),
  );

  const coldRun = {
    nav_ms: round(cold.timing.nav?.responseStart),
    dcl_ms: round(cold.timing.nav?.domContentLoadedEventEnd),
    load_ms: round(cold.timing.nav?.loadEventEnd),
    route_ms: round(cold.timing.routeAt),
    route_source: cold.timing.routeSource,
    requests: cold.net.requests,
    bytes: cold.net.bytes,
    cacheHits: cold.net.cacheHits,
    servedFromCache: cold.net.servedFromCache,
    fromDiskCache: cold.net.fromDiskCache,
    failed: cold.net.failed,
    assets: cold.net.assets,
    resources: cold.timing.resources,
    screen: cold.timing.screen,
    final_url: cold.timing.url,
  };
  const warmRun = {
    nav_ms: round(warm.timing.nav?.responseStart),
    dcl_ms: round(warm.timing.nav?.domContentLoadedEventEnd),
    load_ms: round(warm.timing.nav?.loadEventEnd),
    route_ms: round(warm.timing.routeAt),
    route_source: warm.timing.routeSource,
    requests: warm.net.requests,
    bytes: warm.net.bytes,
    cacheHits: warm.net.cacheHits,
    servedFromCache: warm.net.servedFromCache,
    fromDiskCache: warm.net.fromDiskCache,
    failed: warm.net.failed,
    assets: warm.net.assets,
    resources: warm.timing.resources,
    screen: warm.timing.screen,
    final_url: warm.timing.url,
  };

  const limitations = [];
  if (coldRun.assets["/mail.css"].requests === 0) {
    limitations.push(
      "Home did not request /mail.css (the reader loads it inside a message frame), so there is no transfer size for it on this route.",
    );
  }
  if (warmRun.cacheHits === 0) {
    const shell = ["/app.js", "/app.css"]
      .map((assetPath) => {
        const asset = warmRun.assets[assetPath];
        return `${assetPath} status ${JSON.stringify(asset.statuses)}, ${asset.bytes} transfer bytes`;
      })
      .join("; ");
    limitations.push(
      `The warm reload reported 0 requestServedFromCache events: shell assets are Cache-Control: no-cache and were revalidated rather than used as an unvalidated cache hit (${shell}), while API reads use fetch cache: no-store.`,
    );
  }

  const report = {
    timestamp: new Date().toISOString(),
    version: status.version || "unknown",
    route: "/",
    home_selector: HOME_SELECTOR,
    url: server.baseURL,
    browser: "chromium " + browser.version(),
    build: {
      command: "go build -o e2e/.cache/mailsense ./cmd/mailsense",
      binary: path.relative(root, binary),
    },
    metrics: {
      nav_ms: "navigation start -> responseStart (first byte)",
      dcl_ms: "navigation start -> DOMContentLoaded end",
      load_ms: "navigation start -> load end",
      route_ms: "navigation start -> Home .home-brief mounted (MutationObserver)",
      requests: "CDP requests observed from navigation start through the route mount + 200ms drain",
      bytes: "sum of CDP loadingFinished encodedDataLength",
      cacheHits: "Network.requestServedFromCache events",
    },
    cold: coldRun,
    warm: warmRun,
    limitations,
  };

  await mkdir(path.dirname(outFile), { recursive: true });
  const text = JSON.stringify(report, null, 2) + "\n";
  writeFileSync(outFile, text);
  process.stdout.write(text);
} catch (error) {
  process.stderr.write((error && error.stack ? error.stack : String(error)) + "\n");
  process.exitCode = 1;
} finally {
  if (browser) {
    await browser.close().catch(() => {});
  }
  if (server) {
    await stopServer(server).catch((error) => {
      process.stderr.write("server cleanup failed: " + String(error) + "\n");
      process.exitCode = 1;
    });
  } else {
    if (dataDir) await rm(dataDir, { recursive: true, force: true }).catch(() => {});
    if (sidecarDir) await rm(sidecarDir, { recursive: true, force: true }).catch(() => {});
  }
}
