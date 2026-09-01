import { expect, test, type Page } from "@playwright/test";

const matrix = [
  { name: "phone portrait", width: 320, height: 568 },
  { name: "phone landscape", width: 844, height: 390 },
  { name: "tablet split", width: 768, height: 500 },
  { name: "Discord sidebars", width: 960, height: 540 },
  { name: "laptop", width: 1280, height: 720 },
  { name: "desktop", width: 1920, height: 1080 },
] as const;

class RoomSocket {
  readonly socket: WebSocket;
  latest: any;
  private waiters: Array<{ predicate: (value: any) => boolean; resolve: (value: any) => void }> = [];

  constructor(url: string) {
    this.socket = new WebSocket(url);
    this.socket.addEventListener("message", (event) => {
      const value = JSON.parse(String(event.data));
      if (value.type === "snapshot") this.latest = value;
      const ready = this.waiters.filter((waiter) => waiter.predicate(value));
      this.waiters = this.waiters.filter((waiter) => !ready.includes(waiter));
      ready.forEach((waiter) => waiter.resolve(value));
    });
  }

  async opened() {
    if (this.socket.readyState === WebSocket.OPEN) return;
    await new Promise<void>((resolve, reject) => {
      this.socket.addEventListener("open", () => resolve(), { once: true });
      this.socket.addEventListener("error", () => reject(new Error("room socket failed")), { once: true });
    });
  }

  waitFor(predicate: (value: any) => boolean) {
    if (this.latest && predicate(this.latest)) return Promise.resolve(this.latest);
    return new Promise<any>((resolve, reject) => {
      const waiter = { predicate, resolve };
      this.waiters.push(waiter);
      setTimeout(() => {
        this.waiters = this.waiters.filter((item) => item !== waiter);
        reject(new Error("room setup timed out"));
      }, 5_000);
    });
  }

  send(value: unknown) {
    this.socket.send(JSON.stringify(value));
  }

  close() {
    this.socket.close();
  }
}

async function startedRoom(room: string, playerCount = 8) {
  const clients = Array.from({ length: playerCount }, (_, index) => new RoomSocket(
    `ws://127.0.0.1:4174/ws?room=${room}&user=p${index}&name=Player%20${index}`,
  ));
  await Promise.all(clients.map((client) => client.opened()));
  await clients[0].waitFor((snapshot) => snapshot.players?.length === playerCount);
  clients.forEach((client) => client.send({ type: "set_ready", ready: true }));
  await clients[0].waitFor((snapshot) => snapshot.allReady === true);
  clients[0].send({ type: "start_game" });
  await clients[0].waitFor((snapshot) => snapshot.phase === "initial_peek");
  clients.forEach((client) => client.send({ type: "acknowledge_initial" }));
  await clients[0].waitFor((snapshot) => snapshot.phase === "await_draw");
  return clients;
}

async function expectReachable(page: Page, selector: string) {
  const element = page.locator(selector).first();
  await expect(element).toBeVisible();
  await element.scrollIntoViewIfNeeded();
  const box = await element.boundingBox();
  expect(box, `${selector} has no layout box`).not.toBeNull();
  expect(box!.y).toBeGreaterThanOrEqual(-1);
  expect(box!.y + box!.height).toBeLessThanOrEqual(page.viewportSize()!.height + 1);
}

async function expectNoViewportCropping(page: Page) {
  const report = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    pageWidth: document.documentElement.scrollWidth,
    zeroCards: [...document.querySelectorAll<HTMLElement>(".card-back, .playing-card")]
      .filter((card) => card.getBoundingClientRect().width < 20 || card.getBoundingClientRect().height < 28)
      .length,
  }));
  expect(report.pageWidth - report.viewport, "page overflows horizontally").toBeLessThanOrEqual(1);
  expect(report.zeroCards, "cards collapsed to unusable dimensions").toBe(0);
}

test("lobby ready control remains reachable across the viewport matrix", async ({ page }) => {
  await page.goto(`/?room=lobby-${Date.now()}&user=matrix-lobby&name=Matrix`);
  await expect(page.locator(".lobby-card")).toBeVisible();
  for (const viewport of matrix) {
    await test.step(viewport.name, async () => {
      await page.setViewportSize(viewport);
      await expectReachable(page, ".ready-toggle");
      await expectReachable(page, ".primary-button.wide");
      await expectNoViewportCropping(page);
    });
  }
});

test("eight-player table keeps all critical controls reachable", async ({ page }) => {
  const room = `matrix-${Date.now()}`;
  const clients = await startedRoom(room);
  try {
    await page.goto(`/?room=${room}&user=p0&name=Player%200`);
    await expect(page.locator(".game-surface.table-layout")).toBeVisible();
    await expect(page.locator(".reconnect-banner")).toBeHidden();
    for (const viewport of matrix) {
      await test.step(viewport.name, async () => {
        await page.setViewportSize(viewport);
        await expectReachable(page, ".pile-zone");
        await expectReachable(page, ".my-area");
        await expectNoViewportCropping(page);
      });
    }
  } finally {
    clients.forEach((client) => client.close());
  }
});

test("resize interruption and reconnect recover to an authoritative table", async ({ page, context }) => {
  const room = `recovery-${Date.now()}`;
  const clients = await startedRoom(room, 2);
  try {
    await page.goto(`/?room=${room}&user=p0&name=Player%200`);
    await expect(page.locator(".game-surface.table-layout")).toBeVisible();
    await page.locator(".deck").click();
    await expect(page.locator(".drawn-card-zone")).toBeVisible();
    await page.locator(".discard-wrap.can-discard").click();
    await expect(page.locator(".action-feedback")).toBeVisible();

    await page.setViewportSize({ width: 768, height: 500 });
    await expect(page.locator(".action-feedback")).toBeHidden();
    await expect(page.locator(".action-card-ghost, .action-card-static")).toHaveCount(0);

    await context.setOffline(true);
    await expect(page.locator(".reconnect-banner")).toBeVisible();
    await context.setOffline(false);
    await expect(page.locator(".reconnect-banner")).toBeHidden({ timeout: 8_000 });
    await expect(page.locator(".game-surface.table-layout")).toBeVisible();
    await expect(page.locator(".action-card-ghost, .action-card-static")).toHaveCount(0);
  } finally {
    await context.setOffline(false);
    clients.forEach((client) => client.close());
  }
});
