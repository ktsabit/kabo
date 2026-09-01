import { expect, test, type Page } from "@playwright/test";

const matrix = [
  { name: "phone portrait", width: 320, height: 568 },
  { name: "phone landscape", width: 844, height: 390 },
  { name: "tablet split", width: 768, height: 500 },
  { name: "Discord activity panel", width: 812, height: 660, noPileOverlap: true },
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

async function startedRoom(room: string, playerCount = 8, acknowledgeInitial = true) {
  const clients = Array.from({ length: playerCount }, (_, index) => new RoomSocket(
    `ws://127.0.0.1:4174/ws?room=${room}&user=p${index}&name=Player%20${index}`,
  ));
  await Promise.all(clients.map((client) => client.opened()));
  await clients[0].waitFor((snapshot) => snapshot.players?.length === playerCount);
  clients.forEach((client) => client.send({ type: "set_ready", ready: true }));
  await clients[0].waitFor((snapshot) => snapshot.allReady === true);
  clients[0].send({ type: "start_game" });
  await clients[0].waitFor((snapshot) => snapshot.phase === "initial_peek");
  if (!acknowledgeInitial) return clients;
  clients.forEach((client) => client.send({ type: "acknowledge_initial" }));
  await clients[0].waitFor((snapshot) => snapshot.phase === "await_draw");
  return clients;
}

function firstOccupied(snapshot: any, playerId: string) {
  const slot = snapshot.players.find((player: any) => player.id === playerId)?.cards.find((card: any) => card.occupied);
  if (!slot) throw new Error(`no occupied card for ${playerId}`);
  return { playerId, slot: slot.slot };
}

async function advanceToSwap(clients: RoomSocket[]) {
  for (let step = 0; step < 120; step += 1) {
    const snapshot = clients[0].latest;
    const actorId = snapshot.currentPlayerId;
    const actor = clients[Number(actorId.slice(1))];
    if (snapshot.phase === "await_swap") return snapshot;
    if (snapshot.phase === "await_draw") {
      actor.send({ type: "draw" });
      await clients[0].waitFor((next) => next.phase !== "await_draw");
      continue;
    }
    if (snapshot.phase === "await_choice") {
      actor.send({ type: "discard_drawn" });
      await clients[0].waitFor((next) => next.phase !== "await_choice");
      continue;
    }
    if (snapshot.phase === "await_self_peek") {
      actor.send({ type: "peek", target: firstOccupied(snapshot, actorId) });
      await clients[0].waitFor((next) => next.phase === "reveal_self");
      continue;
    }
    if (snapshot.phase === "await_opponent_peek" || snapshot.phase === "await_king_peek") {
      const opponentId = snapshot.players.find((player: any) => player.id !== actorId).id;
      actor.send({ type: "peek", target: firstOccupied(snapshot, opponentId) });
      await clients[0].waitFor((next) => next.phase.startsWith("reveal_"));
      continue;
    }
    if (snapshot.phase.startsWith("reveal_")) {
      actor.send({ type: "acknowledge_reveal" });
      await clients[0].waitFor((next) => !next.phase.startsWith("reveal_"));
      continue;
    }
    throw new Error(`unexpected phase while seeking swap: ${snapshot.phase}`);
  }
  throw new Error("did not draw a swap power card");
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

async function expectPileClearOfHands(page: Page) {
  const overlaps = await page.evaluate(() => {
    const pile = document.querySelector<HTMLElement>(".pile-zone")?.getBoundingClientRect();
    if (!pile) return [];
    return [...document.querySelectorAll<HTMLElement>(".player-area")]
      .filter((area) => {
        const hand = area.getBoundingClientRect();
        return hand.left < pile.right && hand.right > pile.left && hand.top < pile.bottom && hand.bottom > pile.top;
      })
      .map((area) => area.dataset.playerId ?? "unknown player");
  });
  expect(overlaps, "draw and discard piles overlap a player's hand").toEqual([]);
}

async function elementRect(page: Page, selector: string) {
  return page.locator(selector).evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { x: rect.x, y: rect.y, width: rect.width, height: rect.height };
  });
}

function expectStableRect(before: Awaited<ReturnType<typeof elementRect>>, after: Awaited<ReturnType<typeof elementRect>>) {
  expect(Math.abs(after.x - before.x), "element moved horizontally").toBeLessThanOrEqual(1);
  expect(Math.abs(after.y - before.y), "element moved vertically").toBeLessThanOrEqual(1);
  expect(Math.abs(after.width - before.width), "element width changed").toBeLessThanOrEqual(1);
  expect(Math.abs(after.height - before.height), "element height changed").toBeLessThanOrEqual(1);
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
    await expect(page.locator(".slap-button")).toHaveCount(0);
    const firstHand = page.locator(".player-area").first().locator(".card-row");
    await expect(firstHand).toHaveCount(1);
    await page.getByRole("button", { name: "Switch to grid hand layout" }).click();
    await expect(firstHand).toHaveCount(2);
    await page.reload();
    await expect(page.locator(".game-surface.table-layout")).toBeVisible();
    await expect(page.getByRole("button", { name: "Switch to strip hand layout" })).toBeVisible();
    await expect(page.locator(".player-area").first().locator(".card-row")).toHaveCount(2);
    await page.getByRole("button", { name: "Switch to strip hand layout" }).click();
    await expect(page.locator(".player-area").first().locator(".card-row")).toHaveCount(1);
    for (const viewport of matrix) {
      await test.step(viewport.name, async () => {
        await page.setViewportSize(viewport);
        await expectReachable(page, ".pile-zone");
        await expectReachable(page, ".my-area");
        await expectNoViewportCropping(page);
        if ("noPileOverlap" in viewport) await expectPileClearOfHands(page);
      });
    }
  } finally {
    clients.forEach((client) => client.close());
  }
});

test("round aftermath keeps ready and start controls reachable", async ({ page }) => {
  const room = `aftermath-${Date.now()}`;
  const clients = await startedRoom(room, 2);
  try {
    clients[0].send({ type: "call_end" });
    await clients[0].waitFor((snapshot) => snapshot.phase === "ended");
    await page.goto(`/?room=${room}&user=p0&name=Player%200`);
    await expect(page.locator(".round-summary.next-round-lobby")).toBeVisible();
    await expect(page.locator(".player-area")).toHaveCount(0);
    await expect(page.locator(".final-hands > section")).toHaveCount(2);
    await expect(page.locator(".final-hand-cards .playing-card")).toHaveCount(8);
    for (const viewport of matrix) {
      await test.step(viewport.name, async () => {
        await page.setViewportSize(viewport);
        await expectReachable(page, ".round-summary .ready-toggle");
        await expectReachable(page, ".round-summary .primary-button");
        await expectNoViewportCropping(page);
      });
    }
    await page.locator(".round-summary .ready-toggle").click();
    clients[1].send({ type: "set_ready", ready: true });
    await expect(page.getByRole("button", { name: "Play next round" })).toBeEnabled();
    await page.getByRole("button", { name: "Play next round" }).click();
    await expect(page.locator(".my-area .opening-guide")).toBeVisible();
  } finally {
    clients.forEach((client) => client.close());
  }
});

test("each player sees the private opening cards in their real hand positions", async ({ page }) => {
  const room = `initial-${Date.now()}`;
  const clients = await startedRoom(room, 2, false);
  try {
    await page.goto(`/?room=${room}&user=p0&name=Player%200`);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const myHand = page.locator('.player-area[data-player-id="p0"]');
    await expect(myHand.locator(".playing-card")).toHaveCount(2);
    await expect(myHand.locator('[data-card-ref="p0:0"] .playing-card, [data-card-ref="p0:1"] .playing-card')).toHaveCount(0);
    await expect(myHand.locator('[data-card-ref="p0:2"] .playing-card, [data-card-ref="p0:3"] .playing-card')).toHaveCount(2);
    await expect(myHand.locator(".opening-card-marker")).toHaveText(["Card 3", "Card 4"]);
    await expect(myHand.locator(".opening-guide .turn-countdown")).toContainText(/\d+s/);
    await myHand.getByRole("button", { name: "Ready" }).click();
    await expect(myHand.locator(".opening-guide")).toBeHidden();
    await clients[1].waitFor((snapshot) => snapshot.phase === "initial_peek" && snapshot.players?.find((player: any) => player.id === "p0")?.initialReady === true);
    clients[1].send({ type: "acknowledge_initial" });
    await clients[1].waitFor((snapshot) => snapshot.phase === "await_draw");
    await expect(page.locator(".turn-prompt .turn-countdown")).toContainText(/\d+s/);
  } finally {
    clients.forEach((client) => client.close());
  }
});

test("a power-card swap completes as soon as the second card is selected", async ({ page }) => {
  const room = `swap-${Date.now()}`;
  const clients = await startedRoom(room, 2);
  try {
    const swap = await advanceToSwap(clients);
    const actorId = swap.currentPlayerId;
    const observer = clients[actorId === "p0" ? 1 : 0];
    await page.goto(`/?room=${room}&user=${actorId}&name=${actorId === "p0" ? "Player%200" : "Player%201"}`);
    await expect(page.getByText("Select 2 cards — swap is automatic")).toBeVisible();
    await expect(page.getByRole("button", { name: /confirm swap/i })).toHaveCount(0);
    const targets = page.locator(".power-target .card-button");
    expect(await targets.count()).toBeGreaterThanOrEqual(2);
    await targets.nth(0).click();
    await expect(page.locator(".selection-order")).toHaveText("1");
    await targets.nth(1).click();
    await observer.waitFor((snapshot) => snapshot.phase !== "await_swap");
    await expect(page.getByText("Select 2 cards — swap is automatic")).toBeHidden();
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
    const pileBeforeDraw = await elementRect(page, ".pile-zone");
    await page.locator(".deck").click();
    await expect(page.locator(".drawn-card-zone")).toBeVisible();
    expectStableRect(pileBeforeDraw, await elementRect(page, ".pile-zone"));
    await page.locator(".discard-wrap.can-discard").click();
    await expect(page.locator(".action-card-ghost")).toBeVisible();
    expectStableRect(pileBeforeDraw, await elementRect(page, ".pile-zone"));

    await page.setViewportSize({ width: 768, height: 500 });
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
