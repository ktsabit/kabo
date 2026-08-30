class TestClient {
  constructor(url) {
    this.ws = new WebSocket(url);
    this.latest = undefined;
    this.waiters = [];
    this.ws.onmessage = (event) => {
      const value = JSON.parse(String(event.data));
      if (value.type === "snapshot") this.latest = value;
      const ready = this.waiters.filter(({ predicate }) => predicate(value));
      this.waiters = this.waiters.filter((waiter) => !ready.includes(waiter));
      for (const waiter of ready) waiter.resolve(value);
    };
  }

  opened() {
    if (this.ws.readyState === WebSocket.OPEN) return Promise.resolve();
    return new Promise((resolve, reject) => {
      this.ws.onopen = resolve;
      this.ws.onerror = reject;
    });
  }

  wait(predicate) {
    if (this.latest && predicate(this.latest)) return Promise.resolve(this.latest);
    return new Promise((resolve, reject) => {
      const waiter = { predicate, resolve };
      this.waiters.push(waiter);
      setTimeout(() => {
        this.waiters = this.waiters.filter((item) => item !== waiter);
        reject(new Error("smoke test timed out"));
      }, 3000);
    });
  }

  send(message) {
    this.ws.send(JSON.stringify(message));
  }
}

const base = "ws://127.0.0.1:8080/ws?room=smoke-final";
const ada = new TestClient(`${base}&user=smoke-ada&name=Ada`);
const ben = new TestClient(`${base}&user=smoke-ben&name=Ben`);
await Promise.all([ada.opened(), ben.opened()]);
await Promise.all([ada.wait((s) => s.players?.length === 2), ben.wait((s) => s.players?.length === 2)]);

ada.send({ type: "start_game" });
await Promise.all([ada.wait((s) => s.phase === "initial_peek"), ben.wait((s) => s.phase === "initial_peek")]);
if (ada.latest.reveal?.cards.length !== 2 || ben.latest.reveal?.cards.length !== 2) throw new Error("private opening reveals missing");
if (ada.latest.players.some((p) => p.cards.some((slot) => slot.card))) throw new Error("hidden grid leaked a card");

ada.send({ type: "acknowledge_initial" });
ben.send({ type: "acknowledge_initial" });
await Promise.all([ada.wait((s) => s.phase === "await_draw"), ben.wait((s) => s.phase === "await_draw")]);
const current = ada.latest.currentPlayerId === ada.latest.you.id ? ada : ben;
const observer = current === ada ? ben : ada;
current.send({ type: "draw" });
await current.wait((s) => s.phase === "await_choice" && s.drawnCard);
await observer.wait((s) => s.phase === "await_choice");
if (observer.latest.drawnCard) throw new Error("drawn card leaked to opponent");
current.send({ type: "replace", slot: 0 });
await Promise.all([ada.wait((s) => s.phase === "await_draw" && s.discardTop), ben.wait((s) => s.phase === "await_draw" && s.discardTop)]);
if (ada.latest.discardEventId !== ben.latest.discardEventId) throw new Error("discard event diverged");

ada.ws.close();
ben.ws.close();
console.log("multiplayer smoke test passed");

