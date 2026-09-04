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

const reportedActivityViewports = [
  // The supplied screenshots are Retina captures, so these are their CSS
  // viewport sizes rather than their physical PNG dimensions.
  { name: "reported wide activity", width: 1180, height: 820 },
  { name: "reported short activity", width: 755, height: 373 },
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

async function advancePlayerToReveal(page: Page, clients: RoomSocket[], playerId: string) {
  for (let step = 0; step < 180; step += 1) {
    // Use another player's socket as the authoritative observer because the
    // browser connection replaces the test socket for playerId.
    const observer = clients.find((_, index) => `p${index}` !== playerId)!;
    const snapshot = observer.latest;
    const actorId = snapshot.currentPlayerId;
    const actor = clients[Number(actorId.slice(1))];
    if (snapshot.phase.startsWith("reveal_")) {
      if (actorId === playerId) return snapshot;
      actor.send({ type: "acknowledge_reveal" });
      await observer.waitFor((next) => !next.phase.startsWith("reveal_"));
      continue;
    }
    if (snapshot.phase === "await_draw") {
      if (actorId === playerId) await page.locator(".deck").click();
      else actor.send({ type: "draw" });
      await observer.waitFor((next) => next.phase !== "await_draw");
      continue;
    }
    if (snapshot.phase === "await_choice") {
      if (actorId === playerId) await page.locator(".discard-wrap.can-discard").click();
      else actor.send({ type: "discard_drawn" });
      await observer.waitFor((next) => next.phase !== "await_choice");
      continue;
    }
    if (snapshot.phase === "await_self_peek") {
      if (actorId === playerId) await page.locator(".power-target .card-button").first().click();
      else actor.send({ type: "peek", target: firstOccupied(snapshot, actorId) });
      await observer.waitFor((next) => next.phase === "reveal_self");
      continue;
    }
    if (snapshot.phase === "await_opponent_peek" || snapshot.phase === "await_king_peek") {
      const opponentId = snapshot.players.find((player: any) => player.id !== actorId).id;
      if (actorId === playerId) await page.locator(".power-target .card-button").first().click();
      else actor.send({ type: "peek", target: firstOccupied(snapshot, opponentId) });
      await observer.waitFor((next) => next.phase.startsWith("reveal_"));
      continue;
    }
    if (snapshot.phase === "await_swap") {
      if (actorId === playerId) {
        const targets = page.locator(".power-target .card-button");
        await targets.nth(0).click();
        await targets.nth(1).click();
      } else {
        const occupied = snapshot.players.flatMap((player: any) => player.cards
          .filter((card: any) => card.occupied)
          .map((card: any) => ({ playerId: player.id, slot: card.slot })));
        actor.send({ type: "swap", first: occupied[0], second: occupied[1] });
      }
      await observer.waitFor((next) => next.phase !== "await_swap");
      continue;
    }
    throw new Error(`unexpected phase while seeking reveal: ${snapshot.phase}`);
  }
  throw new Error(`did not reach a reveal for ${playerId}`);
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

async function expectEveryOpponentFullyVisible(page: Page) {
  const clipped = await page.evaluate(() => {
    const viewport = { left: 0, top: 0, right: document.documentElement.clientWidth, bottom: document.documentElement.clientHeight };
    const rail = document.querySelector<HTMLElement>(".opponents-u")?.getBoundingClientRect();
    return [...document.querySelectorAll<HTMLElement>(".opponents-u .player-area")]
      .flatMap((area) => {
        const areaRect = area.getBoundingClientRect();
        const cards = [...area.querySelectorAll<HTMLElement>(".card-back, .playing-card")];
        const rects = [areaRect, ...cards.map((card) => card.getBoundingClientRect())];
        const bounds = rail && getComputedStyle(document.querySelector<HTMLElement>(".opponents-u")!).position === "relative"
          ? { left: Math.max(viewport.left, rail.left), top: Math.max(viewport.top, rail.top), right: Math.min(viewport.right, rail.right), bottom: Math.min(viewport.bottom, rail.bottom) }
          : viewport;
        return rects.some((rect) => rect.left < bounds.left - 1 || rect.right > bounds.right + 1 || rect.top < bounds.top - 1 || rect.bottom > bounds.bottom + 1)
          ? [area.dataset.playerId ?? "unknown player"]
          : [];
      });
  });
  expect(clipped, "opponent hands are clipped by the activity viewport").toEqual([]);
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
  await expect(page.locator(".build-status code")).toHaveText(/^[0-9a-f]{5}$/);
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
    for (const viewport of reportedActivityViewports) {
      await test.step(viewport.name, async () => {
        await page.setViewportSize(viewport);
        await expectEveryOpponentFullyVisible(page);
        await expectNoViewportCropping(page);
      });
    }
  } finally {
    clients.forEach((client) => client.close());
  }
});

test("seven-player round summary keeps its heading visible in the reported portrait activity", async ({ page }) => {
  const room = `reported-aftermath-${Date.now()}`;
  const clients = await startedRoom(room, 7);
  try {
    clients[0].send({ type: "call_end" });
    await clients[0].waitFor((snapshot) => snapshot.phase === "ended");
    await page.setViewportSize({ width: 473, height: 667 });
    await page.goto(`/?room=${room}&user=p0&name=Player%200`);
    const summary = page.locator(".round-summary.next-round-lobby");
    await expect(summary).toBeVisible();
    await summary.locator(".ready-toggle").click();
    await expect(summary.locator(".eyebrow")).toBeInViewport();
    await expect(summary.locator(".final-hands")).toBeInViewport();
    await expect(summary.locator(".ready-roster")).toBeInViewport();
    await expectNoViewportCropping(page);
    const scrollTop = await summary.evaluate((element) => element.scrollTop);
    expect(scrollTop, "the summary scrolled its heading out of view").toBe(0);
  } finally {
    clients.forEach((client) => client.close());
  }
});

test("reveal flips the normal card in place in a compact activity viewport", async ({ page }) => {
  const room = `reported-reveal-${Date.now()}`;
  const clients = await startedRoom(room, 2);
  try {
    await page.setViewportSize({ width: 755, height: 373 });
    await page.goto(`/?room=${room}&user=p0&name=Player%200`);
    await advancePlayerToReveal(page, clients, "p0");
    const revealedSlot = page.locator(".slot-wrap.peek-reveal");
    await expect(revealedSlot).toBeVisible();
    await expect(page.locator(".peek-reveal-overlay")).toHaveCount(0);
    const card = revealedSlot.locator(".peek-card.revealed");
    await expect(card).toBeVisible();
    await card.evaluate(async (element) => {
      await Promise.all(element.getAnimations({ subtree: true }).map((animation) => animation.finished));
    });
    const geometry = await page.evaluate(() => {
      const card = document.querySelector<HTMLElement>(".slot-wrap.peek-reveal .peek-card")!.getBoundingClientRect();
      const slot = document.querySelector<HTMLElement>(".slot-wrap.peek-reveal")!.getBoundingClientRect();
      return {
        card: { left: card.left, top: card.top, right: card.right, bottom: card.bottom, width: card.width, height: card.height },
        slot: { left: slot.left, top: slot.top, width: slot.width, height: slot.height },
        viewport: { width: document.documentElement.clientWidth, height: document.documentElement.clientHeight },
      };
    });
    expect(geometry.card.left).toBeGreaterThanOrEqual(0);
    expect(geometry.card.top).toBeGreaterThanOrEqual(0);
    expect(geometry.card.right).toBeLessThanOrEqual(geometry.viewport.width);
    expect(geometry.card.bottom).toBeLessThanOrEqual(geometry.viewport.height);
    expect(geometry.card.width).toBeCloseTo(geometry.slot.width, 1);
    expect(geometry.card.height).toBeCloseTo(geometry.slot.height, 1);
    expect(geometry.card.left).toBeCloseTo(geometry.slot.left, 1);
    expect(geometry.card.top).toBeCloseTo(geometry.slot.top, 1);
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
    for (const viewport of matrix) {
      await test.step(`opening reveal · ${viewport.name}`, async () => {
        await page.setViewportSize(viewport);
        await expectReachable(page, ".my-area .opening-guide .primary-button");
        await expectNoViewportCropping(page);
      });
    }
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

test("both real player views can ready their opening cards and enter play", async ({ page, context }) => {
  const room = `two-ui-${Date.now()}`;
  const waa = await context.newPage();
  try {
    await page.setViewportSize({ width: 768, height: 500 });
    await waa.setViewportSize({ width: 768, height: 500 });
    await page.goto(`/?room=${room}&user=kai&name=Kai`);
    await waa.goto(`/?room=${room}&user=waa&name=Waa`);
    await page.getByRole("button", { name: "Check" }).click();
    await waa.getByRole("button", { name: "Check" }).click();
    await expect(page.getByRole("button", { name: "Start" })).toBeEnabled();
    await page.getByRole("button", { name: "Start" }).click();

    await expectReachable(page, '.player-area[data-player-id="kai"] .opening-guide .primary-button');
    await expectReachable(waa, '.player-area[data-player-id="waa"] .opening-guide .primary-button');
    await expect(page.locator('.player-area[data-player-id="kai"] .playing-card')).toHaveCount(2);
    await expect(waa.locator('.player-area[data-player-id="waa"] .playing-card')).toHaveCount(2);

    await waa.locator('.player-area[data-player-id="waa"] .opening-guide .primary-button').click();
    await expect(waa.locator(".opening-guide")).toBeHidden();
    await expect(page.locator('.player-area[data-player-id="waa"] .looking-indicator')).toBeHidden();
    await page.locator('.player-area[data-player-id="kai"] .opening-guide .primary-button').click();
    await expect(page.locator(".opening-guide")).toBeHidden();
    await expect(waa.locator(".turn-prompt")).toBeVisible();
    const activePage = await page.locator(".deck").isEnabled() ? page : waa;
    await activePage.locator(".deck").click();
    await expect(activePage.locator(".drawn-card-zone")).toBeVisible();
  } finally {
    await waa.close();
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
