#!/usr/bin/env node
// Check every internal link in a generated static site.
//
// The script serves the site from a local, read-only HTTP server, crawls it
// from the site root, and fails on the first 4xx or 5xx response. It never
// leaves the site origin and never checks an external link.
//
// Limits are deliberate: a site that grows past them is a review signal, not a
// silent truncation. Run with --help for the accepted options.

import { createReadStream } from "node:fs";
import { readFile, stat } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, resolve, sep } from "node:path";
import { parseArgs } from "node:util";

const DEFAULTS = {
  root: "_site",
  prefix: "/",
  maxPages: 80,
  maxLinks: 600,
  maxSeconds: 120,
  timeout: 5000,
};

const CONTENT_TYPES = new Map([
  [".css", "text/css; charset=utf-8"],
  [".gif", "image/gif"],
  [".html", "text/html; charset=utf-8"],
  [".ico", "image/x-icon"],
  [".jpeg", "image/jpeg"],
  [".jpg", "image/jpeg"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".md", "text/markdown; charset=utf-8"],
  [".png", "image/png"],
  [".svg", "image/svg+xml"],
  [".txt", "text/plain; charset=utf-8"],
  [".webp", "image/webp"],
  [".woff", "font/woff"],
  [".woff2", "font/woff2"],
  [".xml", "application/xml; charset=utf-8"],
]);

function usage() {
  return [
    "Usage: node scripts/ci/check-links.mjs [options]",
    "",
    "Options:",
    `  --root <dir>        site root to serve (default: ${DEFAULTS.root})`,
    `  --prefix <path>     URL path the site is served under (default: ${DEFAULTS.prefix})`,
    "  --baseurl-from <f>  read the prefix from a Jekyll config file",
    `  --max-pages <n>     fail after n HTML pages (default: ${DEFAULTS.maxPages})`,
    `  --max-links <n>     fail after n unique internal links (default: ${DEFAULTS.maxLinks})`,
    `  --max-seconds <n>   fail after n seconds of crawling (default: ${DEFAULTS.maxSeconds})`,
    `  --timeout <ms>      per-request timeout (default: ${DEFAULTS.timeout})`,
    "  --help              print this message",
  ].join("\n");
}

function fail(message) {
  console.error(`error: ${message}`);
  process.exit(1);
}

function normalizePrefix(value) {
  if (!value.startsWith("/")) value = `/${value}`;
  if (!value.endsWith("/")) value = `${value}/`;
  return value;
}

async function readBaseurl(path) {
  const config = await readFile(path, "utf8");
  const match = /^baseurl:\s*["']?([^"'\s#]*)["']?\s*(?:#.*)?$/m.exec(config);
  if (match === null) {
    fail(`${path} has no baseurl entry`);
  }
  return match[1];
}

function isInside(root, candidate) {
  return candidate === root || candidate.startsWith(`${root}${sep}`);
}

function createStaticServer(root, prefix) {
  return createServer(async (request, response) => {
    if (request.method !== "GET" && request.method !== "HEAD") {
      response.writeHead(405, { allow: "GET, HEAD" });
      response.end("method not allowed");
      return;
    }

    let url;
    try {
      url = new URL(request.url ?? "/", "http://127.0.0.1");
    } catch {
      response.writeHead(400);
      response.end("bad request");
      return;
    }

    if (url.pathname === prefix.slice(0, -1)) {
      response.writeHead(301, { location: prefix });
      response.end();
      return;
    }

    if (!url.pathname.startsWith(prefix)) {
      response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
      response.end("not found");
      return;
    }

    let relative;
    try {
      relative = decodeURIComponent(url.pathname.slice(prefix.length));
    } catch {
      response.writeHead(400);
      response.end("bad request");
      return;
    }

    if (relative === "" || relative.endsWith("/")) relative += "index.html";
    const file = resolve(root, relative);
    if (!isInside(root, file)) {
      response.writeHead(403);
      response.end("forbidden");
      return;
    }

    let metadata;
    try {
      metadata = await stat(file);
    } catch {
      response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
      response.end("not found");
      return;
    }

    if (!metadata.isFile()) {
      response.writeHead(404, { "content-type": "text/plain; charset=utf-8" });
      response.end("not found");
      return;
    }

    response.writeHead(200, {
      "content-length": metadata.size,
      "content-type": CONTENT_TYPES.get(extname(file)) ?? "application/octet-stream",
    });
    if (request.method === "HEAD") {
      response.end();
      return;
    }
    createReadStream(file).on("error", () => response.destroy()).pipe(response);
  });
}

function extractLinks(html) {
  const links = [];
  const attribute = /\b(?:href|src)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+))/gi;
  for (const match of html.matchAll(attribute)) {
    const value = match[1] ?? match[2] ?? match[3] ?? "";
    if (value !== "") links.push(value);
  }
  return links;
}

function isSkippable(value) {
  return (
    value.startsWith("#") ||
    /^(?:mailto|tel|javascript|data|blob):/i.test(value)
  );
}

async function main() {
  let parsed;
  try {
    parsed = parseArgs({
      options: {
        root: { type: "string" },
        prefix: { type: "string" },
        "baseurl-from": { type: "string" },
        "max-pages": { type: "string" },
        "max-links": { type: "string" },
        "max-seconds": { type: "string" },
        timeout: { type: "string" },
        help: { type: "boolean", short: "h" },
      },
      allowPositionals: false,
    });
  } catch (error) {
    fail(error.message);
  }

  if (parsed.values.help) {
    console.log(usage());
    return;
  }

  const root = resolve(parsed.values.root ?? DEFAULTS.root);
  let prefixValue = parsed.values.prefix;
  if (parsed.values["baseurl-from"] !== undefined) {
    if (prefixValue !== undefined) {
      fail("use either --prefix or --baseurl-from, not both");
    }
    prefixValue = await readBaseurl(parsed.values["baseurl-from"]);
  }
  const prefix = normalizePrefix(prefixValue ?? DEFAULTS.prefix);
  const maxPages = Number(parsed.values["max-pages"] ?? DEFAULTS.maxPages);
  const maxLinks = Number(parsed.values["max-links"] ?? DEFAULTS.maxLinks);
  const maxSeconds = Number(parsed.values["max-seconds"] ?? DEFAULTS.maxSeconds);
  const timeout = Number(parsed.values.timeout ?? DEFAULTS.timeout);

  for (const [name, value] of [
    ["--max-pages", maxPages],
    ["--max-links", maxLinks],
    ["--max-seconds", maxSeconds],
    ["--timeout", timeout],
  ]) {
    if (!Number.isSafeInteger(value) || value <= 0) {
      fail(`${name} must be a positive integer`);
    }
  }

  const rootMetadata = await stat(root).catch(() => null);
  if (rootMetadata === null || !rootMetadata.isDirectory()) {
    fail(`site root ${root} is missing or is not a directory`);
  }

  const server = createStaticServer(root, prefix);
  await new Promise((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(0, "127.0.0.1", resolveListen);
  });

  const address = server.address();
  if (address === null || typeof address === "string") {
    server.close();
    fail("the local server did not report a port");
  }

  const origin = `http://127.0.0.1:${address.port}`;
  const seed = `${origin}${prefix}`;
  const queue = [{ url: seed, from: "site root" }];
  const seen = new Set();
  const broken = [];
  const deadline = Date.now() + maxSeconds * 1000;
  let pages = 0;

  try {
    while (queue.length > 0) {
      if (Date.now() > deadline) {
        fail(`the crawl exceeded ${maxSeconds} seconds; raise --max-seconds`);
      }
      const item = queue.shift();
      if (seen.has(item.url)) continue;
      seen.add(item.url);

      let request;
      try {
        request = await fetch(item.url, {
          redirect: "follow",
          signal: AbortSignal.timeout(timeout),
        });
      } catch (error) {
        broken.push({ url: item.url, from: item.from, status: error.name });
        continue;
      }

      if (!request.url.startsWith(seed)) {
        broken.push({
          url: item.url,
          from: item.from,
          status: `redirected outside ${prefix}`,
        });
        await request.body?.cancel();
        continue;
      }

      if (request.status >= 400) {
        broken.push({ url: item.url, from: item.from, status: request.status });
        await request.body?.cancel();
        continue;
      }

      const contentType = request.headers.get("content-type") ?? "";
      if (!contentType.includes("text/html")) {
        await request.body?.cancel();
        continue;
      }

      pages += 1;
      if (pages > maxPages) {
        fail(`the site has more than ${maxPages} pages; raise --max-pages`);
      }

      const html = await request.text();
      for (const value of extractLinks(html)) {
        if (isSkippable(value)) continue;

        let target;
        try {
          target = new URL(value, item.url);
        } catch {
          broken.push({ url: value, from: item.url, status: "invalid URL" });
          continue;
        }

        if (target.origin !== origin) continue;
        if (target.pathname === prefix.slice(0, -1)) target.pathname = prefix;
        if (!target.pathname.startsWith(prefix)) continue;
        target.hash = "";
        target.search = "";

        if (seen.has(target.href)) continue;
        queue.push({ url: target.href, from: item.url });
        if (seen.size + queue.length > maxLinks) {
          fail(`the site has more than ${maxLinks} internal links; raise --max-links`);
        }
      }
    }
  } finally {
    await new Promise((resolveClose) => server.close(resolveClose));
  }

  if (broken.length > 0) {
    console.error(`error: ${broken.length} broken internal link(s)`);
    for (const item of broken) {
      console.error(`  ${item.status} ${item.url} (linked from ${item.from})`);
    }
    process.exit(1);
  }

  console.log(`checked ${pages} page(s) and ${seen.size} internal link(s): no broken links`);
}

await main();
