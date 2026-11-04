import type { Page } from "@playwright/test";

/**
 * Replace EventSource with a deterministic in-page emitter. The app builds
 * the stream at boot from the bare global, so this is the one seam needed;
 * the fake also provides `EventSource.CLOSED`, which the reconnect branch
 * reads. Use it on tests that drive an event themselves, and on cache specs
 * that must not race a real boot refresh; the boot and log tests keep the
 * real server stream.
 */
// installFakeStreamWithClock installs the fake event stream and a fixed page
// clock: the two tests that advance TTLs need both, and the order (clock before
// navigation, stream before any event) is one rule.
export async function installFakeStreamWithClock(page: Page): Promise<void> {
  await page.clock.install({ time: new Date("2026-11-03T12:00:00Z") });
  await installFakeEventSource(page);
}

export async function installFakeEventSource(page: Page): Promise<void> {
  await page.addInitScript(() => {
    type Listener = (event: { data: string; type: string }) => void;

    class FakeEventSource {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;

      readyState = FakeEventSource.CONNECTING;
      url: string;
      private listeners = new Map<string, Set<Listener>>();

      constructor(url: string) {
        this.url = url;
        const registry = (window as unknown as { __e2eSseSources: FakeEventSource[] })
          .__e2eSseSources;
        registry.push(this);
      }

      addEventListener(type: string, listener: Listener): void {
        if (!this.listeners.has(type)) this.listeners.set(type, new Set());
        this.listeners.get(type)?.add(listener);
      }

      removeEventListener(type: string, listener: Listener): void {
        this.listeners.get(type)?.delete(listener);
      }

      close(): void {
        this.readyState = FakeEventSource.CLOSED;
      }

      emit(type: string, payload?: unknown): void {
        if (type === "open") this.readyState = FakeEventSource.OPEN;
        const data = payload === undefined ? "" : JSON.stringify(payload);
        for (const listener of this.listeners.get(type) ?? []) {
          listener({ data, type });
        }
        for (const listener of this.listeners.get("message") ?? []) {
          listener({ data, type });
        }
      }
    }

    const target = window as unknown as {
      EventSource: typeof FakeEventSource;
      __e2eSseSources: FakeEventSource[];
      __e2eSse: { emit: (type: string, payload?: unknown) => void };
    };
    target.EventSource = FakeEventSource;
    target.__e2eSseSources = [];
    target.__e2eSse = {
      emit(type: string, payload?: unknown) {
        for (const source of target.__e2eSseSources) source.emit(type, payload);
      },
    };
  });
}

export async function emitSse(
  page: Page,
  type: string,
  payload?: unknown,
): Promise<void> {
  await page.evaluate(
    ({ type, payload }) => {
      const target = window as unknown as {
        __e2eSse?: { emit: (type: string, payload?: unknown) => void };
      };
      if (!target.__e2eSse) throw new Error("fake EventSource is not installed");
      target.__e2eSse.emit(type, payload);
    },
    { type, payload },
  );
}
