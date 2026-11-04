// Round 35 first-shell / first-click / API-phase measurement. Starts one real
// Mailsense binary against a prepared store per run, opens Chromium, and
// measures from process start. The launch token stays in session storage and
// is never printed, logged or written to the JSON artifact.
//
//   node bench-first-click.mjs --binary PATH --store DIR --runs 5 --out FILE
//   node bench-first-click.mjs --binary PATH --store DIR --api --out FILE
//
// Click mode records the real request and response timestamps for the learning,
// gate and messages phases; API mode probes the cold (~700 ms after spawn) and
// warm (after boot ready) endpoints without a browser.
import { spawn } from "node:child_process";
import { readFileSync, rmSync, writeFileSync } from "node:fs";
import { mkdir, mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { chromium } from "@playwright/test";

function arg(name, fallback) {
  const index = process.argv.indexOf("--" + name);
  return index >= 0 && process.argv[index + 1] ? process.argv[index + 1] : fallback;
}

const binary = path.resolve(arg("binary", ""));
const store = path.resolve(arg("store", ""));
const runs = Number(arg("runs", "5"));
const idleSeconds = Number(arg("idle", "0"));
const settleSeconds = Number(arg("settle", "60"));
const coldDelayMs = Number(arg("cold-delay", "700"));
const apiMode = process.argv.includes("--api");
const outFile = path.resolve(arg("out", "first-click.json"));
if (!binary || !store) {
  console.error("usage: bench-first-click.mjs --binary PATH --store DIR [--runs N] [--out FILE] [--api]");
  process.exit(2);
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const now = () => Number(process.hrtime.bigint() / 1000000n);

function hostSample() {
  try {
    return {
      load: readFileSync("/proc/loadavg", "utf8").trim(),
      uptime_s: Number(readFileSync("/proc/uptime", "utf8").split(" ")[0]),
    };
  } catch {
    return { load: null, uptime_s: null };
  }
}

function metricMap(payload) {
  return new Map((payload.metrics || []).map((entry) => [entry.name, entry.value]));
}

function cdpDelta(before, after) {
  const left = metricMap(before);
  const right = metricMap(after);
  const seconds = (name) => Math.max(0, ((right.get(name) || 0) - (left.get(name) || 0)) * 1000);
  const count = (name) => Math.max(0, (right.get(name) || 0) - (left.get(name) || 0));
  return {
    script_ms: seconds("ScriptDuration"),
    layout_ms: seconds("LayoutDuration"),
    recalc_style_ms: seconds("RecalcStyleDuration"),
    task_ms: seconds("TaskDuration"),
    layout_count: count("LayoutCount"),
    recalc_style_count: count("RecalcStyleCount"),
  };
}

async function waitForDescriptor(dataDir, deadline) {
  const file = path.join(dataDir, "runtime", "api.json");
  while (now() < deadline) {
    try {
      const parsed = JSON.parse(readFileSync(file, "utf8"));
      if (parsed.url && parsed.token) return parsed;
    } catch {
      // Written atomically after the listener is up.
    }
    await sleep(10);
  }
  throw new Error("the runtime descriptor never appeared");
}

async function waitForExit(proc, ms) {
  if (proc.exitCode !== null) return;
  await new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    proc.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

async function stop(proc, url, token) {
  if (proc.exitCode === null) {
    try {
      await fetch(url + "api/shutdown", {
        method: "POST",
        headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
        body: "{}",
      });
    } catch {
      // The process may already be gone.
    }
  }
  await waitForExit(proc, 10_000);
  if (proc.exitCode === null) proc.kill("SIGTERM");
  await waitForExit(proc, 3_000);
  if (proc.exitCode === null) proc.kill("SIGKILL");
  await waitForExit(proc, 2_000);
}

function startProcess() {
  rmSync(path.join(store, "runtime", "api.json"), { force: true });
  const started = now();
  const proc = spawn(binary, [], {
    env: {
      ...process.env,
      MAILSENSE_DATA_DIR: store,
      GOMAXPROCS: "4",
      DISPLAY: "",
      MAILSENSE_SIDECAR_DIR: sidecar,
      MAILSENSE_SIDECAR_AUTO_INSTALL: "false",
      MAILSENSE_EVIDENCE_AFTER_RUNS: "false",
      MAILSENSE_LAYER1_AFTER_RUNS: "false",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  proc.stdout.on("data", (chunk) => {
    output = (output + String(chunk)).slice(-4000);
  });
  proc.stderr.on("data", (chunk) => {
    output = (output + String(chunk)).slice(-4000);
  });
  return { proc, started, output: () => output };
}

async function getJSON(base, token, endpoint, timeoutMs = 60_000) {
  const started = now();
  const response = await fetch(base + endpoint, {
    headers: { Authorization: `Bearer ${token}` },
    signal: AbortSignal.timeout(timeoutMs),
  });
  const body = await response.arrayBuffer();
  return {
    status: response.status,
    ms: now() - started,
    bytes: body.byteLength,
    wire_bytes: Number(response.headers.get("content-length")) || null,
  };
}

const sidecar = await mkdtemp(path.join(tmpdir(), "mailsense-firstclick-"));
const results = [];

async function runApiProbe() {
  const launched = startProcess();
  const { proc, started } = launched;
  let descriptor;
  try {
    descriptor = await waitForDescriptor(store, started + 30_000);
  } catch (error) {
    await stop(proc, "", "");
    throw new Error(`${error.message}\n${launched.output()}`);
  }
  const listenMs = now() - started;
  const coldPaths = [
    "/api/messages?limit=100&counts=1",
    "/api/messages?limit=100",
    "/api/mailboxes",
    "/api/conversations?limit=30",
    "/api/gate",
    "/api/boot",
  ];
  const record = { listen_ms: listenMs, ready_ms: null, cold: [], warm: {}, error: null };
  try {
    // The listener is normally up well before this; a slow open records the
    // real time instead of pretending the probe was cold at 700 ms.
    await sleep(Math.max(0, coldDelayMs - (now() - started)));
    for (const endpoint of coldPaths) {
      const row = await getJSON(descriptor.url, descriptor.token, endpoint);
      record.cold.push({ path: endpoint, t_ms: now() - started, ...row });
    }
    const deadline = now() + 120_000;
    for (;;) {
      try {
        const response = await fetch(descriptor.url + "/api/boot", {
          headers: { Authorization: `Bearer ${descriptor.token}` },
          signal: AbortSignal.timeout(10_000),
        });
        const view = await response.json();
        if (view.state === "ready") {
          record.ready_ms = now() - started;
          record.boot = { state: view.state, done: view.done, total: view.total, failed: view.failed };
          break;
        }
      } catch (error) {
        if (now() > deadline) throw error;
      }
      if (now() > deadline) throw new Error("the boot never became ready");
      await sleep(50);
    }
    const warmPaths = coldPaths.filter((endpoint) => endpoint !== "/api/boot");
    for (const endpoint of warmPaths) {
      const samples = [];
      for (let i = 0; i < 5; i += 1) {
        const row = await getJSON(descriptor.url, descriptor.token, endpoint);
        samples.push({ status: row.status, ms: row.ms, bytes: row.bytes, wire_bytes: row.wire_bytes });
      }
      record.warm[endpoint] = samples;
    }
    record.boot_warm = await getJSON(descriptor.url, descriptor.token, "/api/boot");
  } catch (error) {
    record.error = String(error && error.message ? error.message : error).slice(0, 500);
  } finally {
    await stop(proc, descriptor.url, descriptor.token);
    rmSync(path.join(store, "runtime", "api.json"), { force: true });
  }
  results.push(record);
  const coldText = record.cold.map((row) => `${row.path}=${row.status}/${row.ms.toFixed(1)}ms/${row.bytes}B`).join(" ");
  const warmText = Object.entries(record.warm)
    .map(([endpoint, samples]) => `${endpoint}=${samples.map((sample) => sample.ms.toFixed(1)).join(",")}`)
    .join(" ");
  console.log(`api listen=${record.listen_ms} ready=${record.ready_ms} cold: ${coldText} warm: ${warmText}` + (record.error ? " error=" + record.error : ""));
}

async function runClick() {
  const browser = await chromium.launch({ headless: true });
  try {
    for (let run = 1; run <= runs; run += 1) {
      const launched = startProcess();
      const { proc, started } = launched;
      let descriptor;
      try {
        descriptor = await waitForDescriptor(store, started + 30_000);
      } catch (error) {
        await stop(proc, "", "");
        throw new Error(`${error.message}\n${launched.output()}`);
      }
      const listenMs = now() - started;

      const context = await browser.newContext({ locale: "en-US", timezoneId: "UTC" });
      const page = await context.newPage();
      await page.addInitScript(
        ({ token }) => {
          try {
            sessionStorage.setItem("mailsense.token", token);
          } catch {
            // Opaque origins cannot use storage.
          }
        },
        { token: descriptor.token },
      );

      const record = {
        run, listen_ms: listenMs, shell_ms: null, skeleton_ms: null, click_ms: null,
        list_ms: null, click_to_list_ms: null, learning_request_ms: null, learning_done_ms: null,
        gate_request_ms: null, gate_done_ms: null, gate_ms: null,
        messages_start_ms: null, messages_done_ms: null, messages_ms: null,
        click_to_messages_ms: null, messages_to_list_ms: null, list_rows: null, dom_nodes: null,
        first_batch_rows: null, rows_at_first_visible: null, first_frame_visible: null,
        first_row_id: null, first_row_text_len: null, dom_nodes_first: null,
        first_frame_at_ms: null, all_rows_at_ms: null, all_rows_ms: null,
        route_cached: null, long_tasks: null, long_tasks_max_ms: null,
        long_tasks_total_ms: null, bench_first_at: null,
        long_tasks_first_count: null, long_tasks_first_max_ms: null,
        long_tasks_deferred_count: null, long_tasks_deferred_max_ms: null,
        long_tasks_deferred_total_ms: null, cdp: null,
        load_before: hostSample(), load_after: null,
        idle_cpu_s: null, boot_hidden_after_idle: null,
        boot_requests: null, boot_requests_during_idle: null, error: null,
      };
      await page.exposeBinding("__benchMailMark", (_source, payload) => {
        if (!payload || typeof payload !== "object") return;
        const at = now() - started;
        if (payload.kind === "first") {
          record.first_frame_at_ms = at;
          record.first_batch_rows = payload.count;
          record.rows_at_first_visible = payload.count;
          record.first_frame_visible = payload.visible;
          record.first_row_id = payload.id;
          record.first_row_text_len = payload.textLength;
          record.dom_nodes_first = payload.domNodes;
        } else if (payload.kind === "all") {
          record.all_rows_at_ms = at;
        }
      });
      const cdp = await context.newCDPSession(page);
      await cdp.send("Performance.enable");
      let metricsBefore = null;

      const requestTimes = [];
      const marks = {};
      let clicked = false;
      const requestMark = (name, at) => {
        if (marks[name] === undefined) marks[name] = at;
      };
      page.on("request", (request) => {
        const pathname = new URL(request.url()).pathname;
        requestTimes.push(Date.now());
        if (pathname === "/api/events") return;
        if (pathname === "/api/learning") requestMark("learning_request", now() - started);
        if (pathname === "/api/gate") requestMark("gate_request", now() - started);
        if (clicked && pathname === "/api/messages") requestMark("messages_request", now() - started);
      });
      page.on("response", (response) => {
        const pathname = new URL(response.url()).pathname;
        if (pathname === "/api/learning") requestMark("learning_done", now() - started);
        if (pathname === "/api/gate") requestMark("gate_done", now() - started);
        if (clicked && pathname === "/api/messages") requestMark("messages_done", now() - started);
      });
      try {
        await page.goto(descriptor.url + "#/hub", { waitUntil: "load", timeout: 20_000 });
        await page.locator("#app").waitFor({ state: "visible", timeout: 20_000 });
        record.shell_ms = now() - started;
        try {
          await page.locator('#main .screen[aria-busy="true"]').first().waitFor({ state: "visible", timeout: 10_000 });
          record.skeleton_ms = now() - started;
        } catch {
          record.skeleton_ms = null;
        }
        await page.evaluate(() => {
          const holder = window;
          holder.__benchFirst = null;
          holder.__benchAllAt = null;
          holder.__benchFirstAt = null;
          holder.__benchLongTasks = [];
          holder.__benchClickAt = performance.now();
          const seen = () => {
            const rows = document.querySelectorAll(".all-mail-row");
            if (!rows.length) return;
            if (!holder.__benchFirst) {
              const first = rows[0];
              const box = first.getBoundingClientRect();
              holder.__benchFirstAt = performance.now();
              holder.__benchFirst = {
                count: rows.length,
                visible: box.width > 0 && box.height > 0,
                id: first.dataset.messageId || "",
                textLength: (first.textContent || "").length,
                domNodes: document.querySelectorAll("*").length,
              };
              window.__benchMailMark({ kind: "first", ...holder.__benchFirst }).catch(() => {});
            }
            if (holder.__benchAllAt === null && rows.length >= 100) {
              holder.__benchAllAt = performance.now();
              window.__benchMailMark({ kind: "all", count: rows.length }).catch(() => {});
            }
          };
          const observer = new MutationObserver(seen);
          observer.observe(document.documentElement, { childList: true, subtree: true });
          holder.__benchObserver = observer;
          if (typeof PerformanceObserver === "function") {
            try {
              const longObserver = new PerformanceObserver((list) => {
                for (const entry of list.getEntries()) {
                  if (entry.startTime >= holder.__benchClickAt) {
                    holder.__benchLongTasks.push({ start: entry.startTime, duration: entry.duration });
                  }
                }
              });
              longObserver.observe({ type: "longtask" });
              holder.__benchLongObserver = longObserver;
            } catch {
              // Long-task entries are optional in this Chromium build.
            }
          }
        });
        metricsBefore = await cdp.send("Performance.getMetrics");
        clicked = true;
        await page.locator('a[data-nav="mail"]').click({ timeout: 10_000 });
        record.click_ms = now() - started;
        await page.locator(".all-mail-row").first().waitFor({ state: "visible", timeout: 30_000 });
        record.list_ms = now() - started;
        record.click_to_list_ms = record.list_ms - record.click_ms;
        record.learning_request_ms = marks.learning_request ?? null;
        record.learning_done_ms = marks.learning_done ?? null;
        record.gate_request_ms = marks.gate_request ?? null;
        record.gate_done_ms = marks.gate_done ?? null;
        if (record.gate_request_ms !== null && record.gate_done_ms !== null) {
          record.gate_ms = record.gate_done_ms - record.gate_request_ms;
        }
        record.messages_start_ms = marks.messages_request ?? null;
        record.messages_done_ms = marks.messages_done ?? null;
        if (record.messages_start_ms !== null && record.messages_done_ms !== null) {
          record.messages_ms = record.messages_done_ms - record.messages_start_ms;
        }
        if (record.messages_start_ms !== null) {
          record.click_to_messages_ms = record.messages_start_ms - record.click_ms;
        }
        if (record.messages_done_ms !== null) {
          record.messages_to_list_ms = record.list_ms - record.messages_done_ms;
        }
        record.route_cached = record.messages_start_ms === null;
        await page.waitForFunction(
          () => document.querySelectorAll(".all-mail-row").length >= 100,
          null,
          { timeout: 30_000 },
        );
        // Let the deferred append's layout and paint finish before the final
        // DOM count and the metric deltas.
        await page.evaluate(
          () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
        );
        const settled = await page.evaluate(() => ({
          rows: document.querySelectorAll(".all-mail-row").length,
          nodes: document.querySelectorAll("*").length,
          first: window.__benchFirst || null,
          firstAt: window.__benchFirstAt || null,
          longTasks: Array.isArray(window.__benchLongTasks) ? window.__benchLongTasks.slice() : null,
        }));
        record.list_rows = settled.rows;
        record.dom_nodes = settled.nodes;
        if (record.first_batch_rows === null && settled.first) {
          record.first_batch_rows = settled.first.count;
          record.rows_at_first_visible = settled.first.count;
          record.first_frame_visible = settled.first.visible;
          record.first_row_id = settled.first.id;
          record.first_row_text_len = settled.first.textLength;
          record.dom_nodes_first = settled.first.domNodes;
        }
        if (settled.longTasks) {
          record.long_tasks = settled.longTasks.length;
          record.long_tasks_max_ms = settled.longTasks.reduce(
            (max, entry) => Math.max(max, Number(entry.duration) || 0),
            0,
          );
          record.long_tasks_total_ms = settled.longTasks.reduce(
            (sum, entry) => sum + (Number(entry.duration) || 0),
            0,
          );
          // Split the observer window at the first real frame: work before the
          // mark built the 24-row first paint, work after it built the deferred
          // rest. The split is attribution, not a second measurement.
          if (typeof settled.firstAt === "number" && settled.firstAt > 0) {
            record.bench_first_at = settled.firstAt;
            const first = settled.longTasks.filter((entry) => Number(entry.start) < settled.firstAt);
            const deferred = settled.longTasks.filter((entry) => Number(entry.start) >= settled.firstAt);
            record.long_tasks_first_count = first.length;
            record.long_tasks_first_max_ms = first.reduce(
              (max, entry) => Math.max(max, Number(entry.duration) || 0),
              0,
            );
            record.long_tasks_deferred_count = deferred.length;
            record.long_tasks_deferred_max_ms = deferred.reduce(
              (max, entry) => Math.max(max, Number(entry.duration) || 0),
              0,
            );
            record.long_tasks_deferred_total_ms = deferred.reduce(
              (sum, entry) => sum + (Number(entry.duration) || 0),
              0,
            );
          }
        }
        if (metricsBefore) {
          const metricsAfter = await cdp.send("Performance.getMetrics");
          record.cdp = cdpDelta(metricsBefore, metricsAfter);
        }
        if (record.all_rows_at_ms === null) record.all_rows_at_ms = now() - started;
        if (record.messages_done_ms !== null) {
          record.all_rows_ms = record.all_rows_at_ms - record.messages_done_ms;
        }
        if (idleSeconds > 0) {
          // Idle means no auto-paginating Mail list: Home has no observer.
          await page.goto(descriptor.url + "#/hub", { waitUntil: "load", timeout: 20_000 });
          await page.locator('html[data-screen="hub"]').waitFor({ state: "attached", timeout: 20_000 });
          // Wait until the one post-ready prefetch has been quiet for 3 s; the
          // measurement window must be idle, not the prefetch tail.
          const quietDeadline = Date.now() + settleSeconds * 1000;
          while (Date.now() < quietDeadline) {
            const last = requestTimes.length ? requestTimes[requestTimes.length - 1] : 0;
            if (Date.now() - last >= 3_000) break;
            await sleep(500);
          }
          const bootBefore = (await page.evaluate(() => performance.getEntriesByType("resource")
            .filter((entry) => entry.name.includes("/api/boot")).length));
          const ticks = () => {
            const fields = readFileSync(`/proc/${proc.pid}/stat`, "utf8").trim().split(/\s+/);
            return Number(fields[13]) + Number(fields[14]);
          };
          const beforeIdle = ticks();
          await sleep(idleSeconds * 1000);
          record.idle_cpu_s = (ticks() - beforeIdle) / 100;
          record.boot_hidden_after_idle = await page.locator("#boot-strip").isHidden();
          const bootAfter = (await page.evaluate(() => performance.getEntriesByType("resource")
            .filter((entry) => entry.name.includes("/api/boot")).length));
          record.boot_requests = bootAfter;
          record.boot_requests_during_idle = bootAfter - bootBefore;
        }
      } catch (error) {
        record.error = String(error && error.message ? error.message : error).slice(0, 500);
      } finally {
        await context.close();
        await stop(proc, descriptor.url, descriptor.token);
        record.load_after = hostSample();
        rmSync(path.join(store, "runtime", "api.json"), { force: true });
      }
      results.push(record);
      console.log(
        `run ${run}: listen=${record.listen_ms} shell=${record.shell_ms} skeleton=${record.skeleton_ms} click=${record.click_ms} list=${record.list_ms} click_to_list=${record.click_to_list_ms}` +
          ` learning=${record.learning_request_ms}->${record.learning_done_ms}` +
          ` gate=${record.gate_request_ms}->${record.gate_done_ms} (${record.gate_ms})` +
          ` messages=${record.messages_start_ms}->${record.messages_done_ms} (${record.messages_ms})` +
          ` c2m=${record.click_to_messages_ms} m2l=${record.messages_to_list_ms} rows=${record.list_rows} nodes=${record.dom_nodes}` +
          ` first_batch=${record.first_batch_rows} first_visible=${record.first_frame_visible} nodes_first=${record.dom_nodes_first}` +
          ` all_rows=${record.all_rows_ms} long_tasks=${record.long_tasks}/${record.long_tasks_max_ms}` +
          ` first_lt=${record.long_tasks_first_count}/${record.long_tasks_first_max_ms}` +
          ` deferred_lt=${record.long_tasks_deferred_count}/${record.long_tasks_deferred_max_ms}/${record.long_tasks_deferred_total_ms}` +
          (record.idle_cpu_s === null ? "" : ` idle_cpu_s=${record.idle_cpu_s.toFixed(2)} boot_hidden=${record.boot_hidden_after_idle} boot_requests=${record.boot_requests} boot_requests_during_idle=${record.boot_requests_during_idle}`) +
          (record.error ? " error=" + record.error : ""),
      );
    }
  } finally {
    await browser.close();
  }
}

try {
  if (apiMode) await runApiProbe();
  else await runClick();
} finally {
  rmSync(sidecar, { recursive: true, force: true });
  await mkdir(path.dirname(outFile), { recursive: true });
  writeFileSync(outFile, JSON.stringify(results, null, 2) + "\n");
}
