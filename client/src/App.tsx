import { Suspense, useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
import { flushSync } from "react-dom";
import { createRoot } from "react-dom/client";
import type {
  ActionView,
  Card,
  CardRef,
  ClientMessage,
  PlayerView,
  RosterPlayerView,
  ServerMessage,
  SnapshotMessage,
} from "../../shared/protocol";
import { cardLabel, isRed } from "./cards";
import { faceFor } from "./cardFaces";
import { initializePlatform, type PlatformSession, websocketURL } from "./platform";

type ConnectionState = "connecting" | "open" | "closed";

type ActionAnchor = {
  element: HTMLElement;
  rect: DOMRect;
};

type ActionGeometry = {
  first?: ActionAnchor;
  second?: ActionAnchor;
  target?: ActionAnchor;
  discard?: ActionAnchor;
  drawn?: ActionAnchor;
};

type QueuedAction = {
  action: ActionView;
  snapshot: SnapshotMessage;
};

type ActionVisualHold = {
  id: number;
  release: () => void;
};

const DOUBLE_TAP_WINDOW = 360;

function App() {
  const [platform, setPlatform] = useState<PlatformSession>();
  const [snapshot, setSnapshot] = useState<SnapshotMessage>();
  const [connection, setConnection] = useState<ConnectionState>("connecting");
  const [problem, setProblem] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [swapSelection, setSwapSelection] = useState<CardRef[]>([]);
  const [actionAnimating, setActionAnimating] = useState(false);
  const socket = useRef<WebSocket | undefined>(undefined);
  const renderedSnapshot = useRef<SnapshotMessage | undefined>(undefined);
  const lastActionID = useRef(0);
  const actionQueue = useRef<QueuedAction[]>([]);
  const animationRunning = useRef(false);
  const runningActionID = useRef<number | undefined>(undefined);
  const animationPumpFrame = useRef<number | undefined>(undefined);
  const actionVisualHold = useRef<ActionVisualHold | undefined>(undefined);
  const deferredSnapshot = useRef<SnapshotMessage | undefined>(undefined);

  const renderSnapshot = (next: SnapshotMessage, synchronous = false) => {
    renderedSnapshot.current = next;
    if (synchronous) flushSync(() => setSnapshot(next));
    else setSnapshot(next);
  };

  const pumpActionQueue = () => {
    animationPumpFrame.current = undefined;
    if (animationRunning.current) return;
    const queued = actionQueue.current.shift();
    if (!queued) return;

    const geometry = captureActionGeometry(queued.action);
    const heldActivePlayerID = renderedSnapshot.current?.currentPlayerId;
    document.documentElement.classList.add("action-in-flight");
    animationRunning.current = true;
    runningActionID.current = queued.action.id;
    renderedSnapshot.current = queued.snapshot;
    flushSync(() => {
      setActionAnimating(true);
      setSnapshot(queued.snapshot);
    });
    const hold: ActionVisualHold = { id: queued.action.id, release: holdActionVisuals(queued.action, heldActivePlayerID) };
    actionVisualHold.current?.release();
    actionVisualHold.current = hold;

    void animateAction(queued.action, geometry)
      .catch((error: unknown) => console.error("Kabo action animation failed", error))
      .finally(() => {
        const deferred = deferredSnapshot.current;
        if (deferred) {
          deferredSnapshot.current = undefined;
          renderSnapshot(deferred, true);
        }
        if (actionVisualHold.current?.id === queued.action.id) {
          actionVisualHold.current.release();
          actionVisualHold.current = undefined;
        }
        animationRunning.current = false;
        runningActionID.current = undefined;
        if (actionQueue.current.length === 0) {
          document.documentElement.classList.remove("action-in-flight");
          flushSync(() => setActionAnimating(false));
        } else {
          scheduleActionPump();
        }
      });
  };

  const scheduleActionPump = () => {
    if (animationRunning.current || animationPumpFrame.current !== undefined) return;
    animationPumpFrame.current = window.requestAnimationFrame(pumpActionQueue);
  };

  const queueSnapshot = (next: SnapshotMessage) => {
    const action = next.action;
    if (!renderedSnapshot.current) {
      lastActionID.current = action?.id ?? 0;
      renderSnapshot(next);
      return;
    }

    if (!action) {
      if (animationRunning.current || actionQueue.current.length > 0) deferredSnapshot.current = next;
      else renderSnapshot(next);
      return;
    }

    // A process restart can recreate a room with fresh cursors. Do not let a
    // stale local cursor suppress every action from the new server instance.
    if (action.id < lastActionID.current) {
      actionQueue.current.length = 0;
      lastActionID.current = action.id;
      if (animationRunning.current) deferredSnapshot.current = next;
      else renderSnapshot(next);
      return;
    }

    if (action.id === lastActionID.current) {
      const waiting = actionQueue.current.find((item) => item.action.id === action.id);
      if (waiting) waiting.snapshot = next;
      else if (runningActionID.current === action.id) deferredSnapshot.current = next;
      else if (!animationRunning.current && actionQueue.current.length === 0) renderSnapshot(next);
      return;
    }

    lastActionID.current = action.id;
    actionQueue.current.push({ action, snapshot: next });
    scheduleActionPump();
  };

  useEffect(() => {
    initializePlatform().then(setPlatform).catch((error: unknown) => {
      console.error("Kabo Activity initialization failed", error);
      setProblem(readableError(error));
      setConnection("closed");
    });
  }, []);

  useEffect(() => {
    if (!platform) return;
    let retry: number | undefined;
    let stopped = false;
    let attempt = 0;

    const connect = () => {
      if (stopped) return;
      setConnection("connecting");
      const ws = new WebSocket(websocketURL(platform));
      socket.current = ws;
      ws.onopen = () => {
        attempt = 0;
        setConnection("open");
        setProblem(undefined);
      };
      ws.onmessage = (event) => {
        const message = JSON.parse(String(event.data)) as ServerMessage;
        if (message.type === "snapshot") {
          queueSnapshot(message);
          return;
        }
        if (message.type === "error") {
          if (message.code === "wrong_slap") return;
          setProblem(message.message);
          window.setTimeout(() => setProblem(undefined), 3200);
        } else {
          setNotice(message.message);
        }
      };
      ws.onclose = () => {
        if (stopped) return;
        setConnection("closed");
        attempt += 1;
        retry = window.setTimeout(connect, Math.min(5000, 500 * 2 ** attempt));
      };
      ws.onerror = () => ws.close();
    };

    connect();
    return () => {
      stopped = true;
      if (retry) window.clearTimeout(retry);
      socket.current?.close();
    };
  }, [platform]);

  useEffect(() => {
    setSwapSelection([]);
  }, [snapshot?.phase]);

  const send = useCallback((message: ClientMessage) => {
    if (socket.current?.readyState !== WebSocket.OPEN) {
      setProblem("The table is reconnecting. Try again in a moment.");
      return;
    }
    socket.current.send(JSON.stringify(message));
  }, []);

  const revealKey = snapshot?.reveal
    ? `${snapshot.reveal.kind}:${snapshot.reveal.cards.map((item) => `${refKey(item.target)}:${item.card.id}`).join(",")}`
    : "";

  useEffect(() => {
    if (!snapshot?.reveal || snapshot.reveal.kind === "initial") return;
    const delay = snapshot.deadlineAt ? Math.max(0, snapshot.deadlineAt - Date.now()) : 3000;
    const timer = window.setTimeout(() => send({ type: "acknowledge_reveal" }), delay);
    return () => window.clearTimeout(timer);
  }, [send, revealKey, snapshot?.deadlineAt]);

  const chooseSwap = (target: CardRef) => {
    if (!snapshot || snapshot.phase !== "await_swap" || snapshot.currentPlayerId !== snapshot.you.id) return;
    const exists = swapSelection.some((item) => sameRef(item, target));
    const next = exists ? swapSelection.filter((item) => !sameRef(item, target)) : [...swapSelection, target].slice(-2);
    setSwapSelection(next);
  };

  const confirmSwap = () => {
    if (!snapshot || snapshot.phase !== "await_swap" || snapshot.currentPlayerId !== snapshot.you.id || swapSelection.length !== 2) return;
    send({ type: "swap", first: swapSelection[0], second: swapSelection[1] });
  };

  const cardAction = (target: CardRef) => {
    if (!snapshot) return;
    const mine = target.playerId === snapshot.you.id;
    const myTurn = snapshot.currentPlayerId === snapshot.you.id;
    if (snapshot.phase === "await_choice" && myTurn && mine) {
      send({ type: "replace", slot: target.slot });
      return;
    }
    if (myTurn && snapshot.phase === "await_self_peek" && mine) {
      send({ type: "peek", target });
      return;
    }
    if (myTurn && (snapshot.phase === "await_opponent_peek" || snapshot.phase === "await_king_peek") && !mine) {
      send({ type: "peek", target });
      return;
    }
    if (myTurn && snapshot.phase === "await_swap") {
      chooseSwap(target);
    }
  };

  const slap = (target: CardRef) => {
    if (snapshot?.youRole !== "active" || !snapshot.discardEventId) return;
    send({ type: "slap", eventId: snapshot.discardEventId, target });
  };

  const canDiscardDrawn = Boolean(snapshot?.drawnCard && snapshot.phase === "await_choice" && snapshot.currentPlayerId === snapshot.you.id);
  const discardDrawn = () => {
    if (canDiscardDrawn) send({ type: "discard_drawn" });
  };

  const dropToSlap = (event: React.DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    try {
      const target = JSON.parse(event.dataTransfer.getData("application/x-cambio-card")) as CardRef;
      slap(target);
    } catch {
      // Ignore non-card drags.
    }
  };

  const copyInvite = async () => {
    if (!platform) return;
    const url = new URL(window.location.href);
    url.searchParams.set("room", platform.roomId);
    url.searchParams.delete("user");
    url.searchParams.delete("name");
    await navigator.clipboard.writeText(url.toString());
    setNotice("Table link copied");
    window.setTimeout(() => setNotice(undefined), 1800);
  };

  if (!platform || !snapshot) {
    return (
      <main className="loading-screen">
        <div className="brand-mark bear-logo" role="img" aria-label="Kabo bear" />
        <h1>Kabo</h1>
        <p>{problem ?? "Pulling up a seat…"}</p>
        <span className={`connection-dot ${connection}`} />
      </main>
    );
  }

  const me = snapshot.players.find((player) => player.id === snapshot.you.id);
  const opponents = snapshot.players.filter((player) => player.id !== snapshot.you.id);
  const isSpectator = snapshot.youRole === "spectator";
  const isMyTurn = snapshot.currentPlayerId === snapshot.you.id;
  const winnerNames = snapshot.players.filter((player) => snapshot.winnerIds?.includes(player.id)).map((player) => player.name);
  const loserNames = snapshot.players.filter((player) => snapshot.loserIds?.includes(player.id)).map((player) => player.name);
  const showingAftermath = snapshot.phase === "ended" && !actionAnimating;

  return (
    <main className="app-shell">
      <header className="topbar">
        <div className="room-chip">Table <b>{platform.roomId.slice(-8)}</b></div>
        <div className="topbar-actions">
          {snapshot.phase !== "lobby" && snapshot.phase !== "ended" && <NextRoundRoster snapshot={snapshot} send={send} />}
          {platform.mode === "browser" && <button className="ghost-button" onClick={copyInvite}>Invite</button>}
        </div>
      </header>

      <section className={`game-surface ${snapshot.phase !== "lobby" ? "table-layout" : ""} ${showingAftermath ? "round-ended" : ""}`}>
        {snapshot.phase === "lobby" ? (
          <Lobby snapshot={snapshot} platform={platform} send={send} />
        ) : (
          <>
            <div className={`opponents opponents-u opponents-${opponents.length}`} aria-label="Opponents">
              {opponents.map((player, index) => (
                <PlayerArea
                  key={player.id}
                  style={uSeatStyle(index, opponents.length)}
                  player={player}
                  snapshot={snapshot}
                  selected={swapSelection}
                  onCard={cardAction}
                  onSlap={slap}
                  onGift={(slot) => send({ type: "gift", sourceSlot: slot })}
                  onInitialDone={() => send({ type: "acknowledge_initial" })}
                  canInteract={!isSpectator && snapshot.phase !== "ended"}
                  revealEnded={showingAftermath}
                />
              ))}
            </div>

            <div className={`table-center slap-dropzone ${showingAftermath ? "aftermath-center" : ""}`} onDragOver={(event) => event.preventDefault()} onDrop={dropToSlap}>
              {showingAftermath ? (
                <RoundSummary snapshot={snapshot} winners={winnerNames} losers={loserNames} send={send} canStart={!isSpectator} />
              ) : (
                <>
                  <div className="pile-zone">
                    <button className="deck" disabled={!isMyTurn || snapshot.phase !== "await_draw"} onClick={() => send({ type: "draw" })}>
                      <span>{snapshot.drawPileCount}</span>
                      <small>DRAW</small>
                    </button>
                    <div className={`drawn-card-position ${snapshot.hasDrawnCard ? "occupied" : ""}`}>
                      {snapshot.hasDrawnCard && (
                        <div className="drawn-card-zone">
                          {snapshot.drawnCard ? <PlayingCard card={snapshot.drawnCard} compact /> : <CardBack compact />}
                        </div>
                      )}
                    </div>
                    <div
                      className={`discard-wrap ${canDiscardDrawn ? "can-discard" : ""}`}
                      role={canDiscardDrawn ? "button" : undefined}
                      tabIndex={canDiscardDrawn ? 0 : undefined}
                      aria-label={canDiscardDrawn ? "Discard drawn card" : "Discard pile"}
                      onClick={canDiscardDrawn ? discardDrawn : undefined}
                      onKeyDown={(event) => {
                        if (canDiscardDrawn && (event.key === "Enter" || event.key === " ")) {
                          event.preventDefault();
                          discardDrawn();
                        }
                      }}
                    >
                      {snapshot.discardTop ? <PlayingCard card={snapshot.discardTop} compact /> : <div className="empty-discard">Discard</div>}
                      <small>DISCARD</small>
                    </div>
                  </div>
                  <TurnPrompt snapshot={snapshot} isMyTurn={isMyTurn} />
                  {isMyTurn && snapshot.phase === "await_swap" && swapSelection.length > 0 && (
                    <div className="swap-controls" aria-label="Swap selection controls">
                      <span>{swapSelection.length}/2</span>
                      <button className="swap-clear" onClick={() => setSwapSelection([])}>Clear</button>
                      <button className="primary-button swap-confirm" disabled={swapSelection.length !== 2} onClick={confirmSwap}>Confirm swap</button>
                    </div>
                  )}
                </>
              )}
            </div>

            {me && (
              <div className="my-area">
                <PlayerArea player={me} snapshot={snapshot} selected={swapSelection} onCard={cardAction} onSlap={slap} onGift={(slot) => send({ type: "gift", sourceSlot: slot })} onInitialDone={() => send({ type: "acknowledge_initial" })} mine canInteract={snapshot.phase !== "ended"} revealEnded={showingAftermath} />
                {isMyTurn && snapshot.phase === "await_draw" && (
                  <button className="kabo-button" onClick={() => send({ type: "call_end" })}>KABO!</button>
                )}
              </div>
            )}
          </>
        )}
      </section>

      {problem && <div className="toast error" role="alert">{problem}</div>}
      {notice && <div className="toast">{notice}</div>}
      <span className={`connection-dot table-connection ${connection}`} aria-label={connection === "open" ? "Connected" : "Reconnecting"} />
    </main>
  );
}

function NextRoundRoster({ snapshot, send }: { snapshot: SnapshotMessage; send: (message: ClientMessage) => void }) {
  const disabled = !snapshot.nextRoundJoined && snapshot.nextRoundFull;

  return (
    <details className="next-round-menu">
      <summary className="next-round-trigger" aria-label="Next round players">
        <strong>{snapshot.nextRoundPlayers.length}/8</strong>
      </summary>
      <div className="next-round-popover">
        <div className="next-round-list">
          {snapshot.nextRoundPlayers.map((player, index) => (
            <RosterRow key={player.id} player={player} index={index} />
          ))}
          {snapshot.nextRoundPlayers.length === 0 && <div className="next-round-empty">—</div>}
        </div>
        <button
          className={`join-next-round ${snapshot.nextRoundJoined ? "joined" : ""}`}
          disabled={disabled}
          aria-pressed={snapshot.nextRoundJoined}
          onClick={() => send({ type: "set_next_round", joinNextRound: !snapshot.nextRoundJoined })}
        >
          {snapshot.nextRoundJoined ? "Leave" : "Join"}
        </button>
      </div>
    </details>
  );
}

function RosterRow({ player, index }: { player: RosterPlayerView; index: number }) {
  return (
    <div className="next-round-player">
      <span className="roster-number">{index + 1}</span>
      <span className={`avatar avatar-${hash(player.id) % 5}`}>{player.name.slice(0, 1).toUpperCase()}</span>
      <b>{player.name}</b>
      <span className={`presence-dot ${player.connected ? "connected" : "away"}`} aria-label={player.connected ? "Connected" : "Disconnected"} />
    </div>
  );
}

function ReadyRoster({ snapshot, send }: { snapshot: SnapshotMessage; send: (message: ClientMessage) => void }) {
  return (
    <div className="ready-roster">
      {snapshot.nextRoundPlayers.map((player, index) => {
        const self = player.id === snapshot.you.id;
        return (
          <div className="ready-row" key={player.id}>
            <span className="roster-number">{index + 1}</span>
            <span className={`avatar avatar-${hash(player.id) % 5}`}>{player.name.slice(0, 1).toUpperCase()}</span>
            <b>{self ? "You" : player.name}</b>
            {self ? (
              <button className={`ready-toggle ${snapshot.youReady ? "ready" : ""}`} aria-pressed={snapshot.youReady} onClick={() => send({ type: "set_ready", ready: !snapshot.youReady })}>
                {snapshot.youReady ? "Ready" : "Check"}
              </button>
            ) : (
              <small className={player.ready ? "ready" : player.connected ? "not-ready" : "away"}>
                {player.ready ? "Ready" : player.connected ? "—" : "Away"}
              </small>
            )}
          </div>
        );
      })}
      {snapshot.nextRoundPlayers.length === 0 && <div className="waiting-seat">Waiting for players…</div>}
    </div>
  );
}

function Lobby({ snapshot, platform, send }: { snapshot: SnapshotMessage; platform: PlatformSession; send: (message: ClientMessage) => void }) {
  const canStart = snapshot.youRole === "active" && snapshot.allReady;
  const players = snapshot.nextRoundPlayers;
  const readyCount = players.filter((player) => player.ready).length;
  return (
    <div className="lobby-card">
      <h1 className="lobby-title">KABO</h1>
      <div className="lobby-players">
        <div className="ready-summary"><strong>{readyCount}/{players.length}</strong><span>ready</span></div>
        <ReadyRoster snapshot={snapshot} send={send} />
        {players.length < 2 && <div className="waiting-seat">Need 2 players</div>}
      </div>
      <button className="primary-button wide" disabled={!canStart || players.length < 2} onClick={() => send({ type: "start_game" })}>
        {players.length < 2 ? "Need 2" : canStart ? "Start" : "Ready up"}
      </button>
      <small>{platform.mode === "discord" ? `${platform.participants ?? snapshot.players.length} in this Activity` : "Open the invite in another browser profile to join."}</small>
    </div>
  );
}

function RoundSummary({ snapshot, winners, losers, send, canStart }: { snapshot: SnapshotMessage; winners: string[]; losers: string[]; send: (message: ClientMessage) => void; canStart: boolean }) {
  const players = snapshot.nextRoundPlayers;
  const readyCount = players.filter((player) => player.ready).length;
  const starter = snapshot.nextStarterId ? rosterName(snapshot, snapshot.nextStarterId) : undefined;
  const canJoin = !snapshot.nextRoundJoined && !snapshot.nextRoundFull;
  return (
    <div className="round-summary next-round-lobby">
      <span className="eyebrow">ROUND COMPLETE</span>
      <strong>{winners.join(" & ")} {winners.length === 1 ? "wins" : "tie"}</strong>
      {losers.length > 0 && <small className="loser-note">Loser · {losers.join(" & ")}</small>}
      <small>{endReason(snapshot.endReason)}</small>
      {starter && <small className="starter-note">{starter} starts</small>}
      {canJoin && <button className="join-next-round summary-join" onClick={() => send({ type: "set_next_round", joinNextRound: true })}>Join</button>}
      {players.length > 0 && (
        <>
          <div className="ready-summary"><strong>{readyCount}/{players.length}</strong><span>ready</span></div>
          <ReadyRoster snapshot={snapshot} send={send} />
        </>
      )}
      {canStart ? <button className="primary-button" disabled={!snapshot.allReady} onClick={() => send({ type: "start_game" })}>{snapshot.allReady ? "Start" : "Ready up"}</button> : <small>Waiting for a player to start.</small>}
    </div>
  );
}

function PlayerArea({ player, snapshot, selected, onCard, onSlap, onGift, onInitialDone, style, mine = false, canInteract = true, revealEnded = true }: {
  player: PlayerView;
  snapshot: SnapshotMessage;
  selected: CardRef[];
  onCard: (target: CardRef) => void;
  onSlap: (target: CardRef) => void;
  onGift: (slot: number) => void;
  onInitialDone: () => void;
  style?: CSSProperties;
  mine?: boolean;
  canInteract?: boolean;
  revealEnded?: boolean;
}) {
  const active = snapshot.currentPlayerId === player.id && snapshot.phase !== "initial_peek" && snapshot.phase !== "ended";
  const stillLooking = snapshot.phase === "initial_peek" && player.initialReady === false;
  const drawing = active && snapshot.phase === "await_choice";
  const giftMode = canInteract && snapshot.phase === "await_gift" && snapshot.pendingGift?.slapperId === snapshot.you.id && player.id === snapshot.you.id;
  const tap = useRef<{ key: string; at: number; timer?: number } | undefined>(undefined);
  const dragging = useRef(false);
  const area = useRef<HTMLElement>(null);

  useEffect(() => () => {
    if (tap.current?.timer) window.clearTimeout(tap.current.timer);
  }, []);

  useEffect(() => {
    if (tap.current?.timer) window.clearTimeout(tap.current.timer);
    tap.current = undefined;
  }, [snapshot.phase, snapshot.discardEventId, snapshot.action?.id]);

  const handleTap = (target: CardRef) => {
    if (dragging.current || !canInteract) return;
    if (snapshot.phase === "await_swap" && snapshot.currentPlayerId === snapshot.you.id) {
      const key = refKey(target);
      const now = Date.now();
      if (tap.current?.key === key && now - tap.current.at < DOUBLE_TAP_WINDOW) {
        if (tap.current.timer) window.clearTimeout(tap.current.timer);
        tap.current = undefined;
        onSlap(target);
        return;
      }
      if (tap.current?.timer) window.clearTimeout(tap.current.timer);
      onCard(target);
      const timer = window.setTimeout(() => {
        if (tap.current?.key === key) tap.current = undefined;
      }, DOUBLE_TAP_WINDOW);
      tap.current = { key, at: now, timer };
      return;
    }
    const key = refKey(target);
    const now = Date.now();
    if (tap.current?.key === key && now - tap.current.at < DOUBLE_TAP_WINDOW) {
      if (tap.current.timer) window.clearTimeout(tap.current.timer);
      tap.current = undefined;
      if (snapshot.phase === "await_swap" && snapshot.currentPlayerId === snapshot.you.id) onCard(target);
      else onSlap(target);
      return;
    }
    if (tap.current?.timer) window.clearTimeout(tap.current.timer);
    const timer = window.setTimeout(() => {
      if (dragging.current) {
        tap.current = undefined;
        return;
      }
      if (giftMode) onGift(target.slot);
      else onCard(target);
      tap.current = undefined;
    }, DOUBLE_TAP_WINDOW);
    tap.current = { key, at: now, timer };
  };

  const initialRevealHere = mine && snapshot.reveal?.kind === "initial";
  const hasPeekReveal = snapshot.reveal?.kind !== "initial" && Boolean(snapshot.reveal?.cards.some((item) => item.target.playerId === player.id));
  const penaltyPending = snapshot.phase !== "ended" && snapshot.action?.kind === "wrong_slap" && snapshot.action.actorId === player.id;
  const winner = revealEnded && snapshot.phase === "ended" && snapshot.winnerIds?.includes(player.id);
  const arrangedCards = [...player.cards].sort((a, b) => handVisualOrder(a.slot) - handVisualOrder(b.slot));
  return (
    <section ref={area} data-player-id={player.id} style={style} className={`player-area ${mine ? "mine" : ""} ${active ? "active" : ""} ${winner ? "winner" : ""} ${hasPeekReveal ? "has-peek-reveal" : ""} ${revealEnded && snapshot.phase === "ended" ? "cards-revealed" : ""} ${snapshot.phase === "await_choice" && mine ? "replace-mode" : ""}`}>
      <div className="player-heading">
        <span className={`avatar avatar-${hash(player.id) % 5}`}>{player.name.slice(0, 1).toUpperCase()}</span>
        <div><b title={player.name}>{mine ? "You" : player.name}</b>{revealEnded && snapshot.phase === "ended" ? <small>{player.score} pts</small> : !player.connected && <small>Disconnected</small>}</div>
        {stillLooking && (
          <span className="looking-indicator" role="status" aria-label={`${mine ? "You are" : `${player.name} is`} still looking at the opening cards`} title="Still looking">
            <i />
          </span>
        )}
        {drawing && (
          <span className="drawing-indicator" role="status" aria-label={`${mine ? "You are" : `${player.name} is`} drawing a card`} title="Drawing a card">
            <i />
          </span>
        )}
      </div>
      <div className={`card-grid card-count-${player.cards.length}`}>
        {arrangedCards.map((slot) => {
          const target = { playerId: player.id, slot: slot.slot };
          const selectionOrder = selected.findIndex((item) => sameRef(item, target)) + 1;
          const isSelected = selectionOrder > 0;
          const revealed = revealAt(snapshot, target);
          const peekReveal = Boolean(revealed && snapshot.reveal?.kind !== "initial");
          const power = canInteract ? powerHint(snapshot, target, slot.occupied) : undefined;
          const peekedByOther = snapshot.publicPeek?.viewerId !== snapshot.you.id && snapshot.publicPeek && sameRef(snapshot.publicPeek.target, target);
          return (
            <div className={`slot-wrap ${!slot.occupied ? "empty-slot-anchor" : ""} ${penaltyPending && slot.slot === player.cards.length - 1 ? "penalty-card-pending" : ""} ${isSelected ? "selected" : ""} ${revealed ? "is-revealed" : ""} ${peekReveal ? "peek-reveal" : ""} ${peekedByOther ? "peek-observed" : ""} ${power ? `power-target power-${power}` : ""}`} key={slot.slot} data-card-ref={refKey(target)}>
              <button
                className="card-button"
                disabled={!slot.occupied || !canInteract}
                draggable={slot.occupied && canInteract}
                aria-label={`${mine ? "Your" : player.name}'s card ${slot.slot + 1}${power ? ` · ${power === "peek" ? "peek target" : "swap target"}` : ""}`}
                onClick={(event) => { if (event.detail === 0) giftMode ? onGift(slot.slot) : onCard(target); }}
                onPointerDown={(event) => {
                  if (event.isPrimary && event.button === 0) handleTap(target);
                }}
                onDragStart={(event) => {
                  dragging.current = true;
                  event.dataTransfer.setData("application/x-cambio-card", JSON.stringify(target));
                }}
                onDragEnd={() => window.setTimeout(() => { dragging.current = false; }, 0)}
              >
                {slot.occupied
                  ? slot.card && revealEnded
                    ? <PlayingCard card={slot.card} compact flipped />
                    : <PeekableCard card={revealed} compact />
                  : <div className="empty-slot" />}
              </button>
              {isSelected && <span className="selection-order" aria-hidden="true">{selectionOrder}</span>}
              {canInteract && slot.occupied && snapshot.discardTop && snapshot.phase !== "initial_peek" && snapshot.phase !== "ended" && (
                <button className="slap-button" onClick={() => onSlap(target)} aria-label={`Slap ${player.name}'s card ${slot.slot + 1}`}>↯</button>
              )}
              {giftMode && slot.occupied && (
                <button className="gift-button" onClick={() => onGift(slot.slot)}>GIVE</button>
              )}
              {revealed && snapshot.reveal?.kind !== "initial" && <span className="peek-timer" aria-label="Hides in three seconds" />}
              {peekedByOther && <span className="peek-indicator" role="status" aria-label={`${snapshot.players.find((item) => item.id === snapshot.publicPeek?.viewerId)?.name ?? "A player"} is peeking at this card`}><i /></span>}
            </div>
          );
        })}
      </div>
      {initialRevealHere && <button className="reveal-done" onClick={onInitialDone}>Ready</button>}
    </section>
  );
}

function TurnPrompt({ snapshot, isMyTurn }: { snapshot: SnapshotMessage; isMyTurn: boolean }) {
  const canGift = snapshot.phase === "await_gift" && snapshot.pendingGift?.slapperId === snapshot.you.id;
  const waitingForInitialReady = snapshot.phase === "initial_peek" && snapshot.reveal?.kind === "initial" && snapshot.players.some((player) => player.id === snapshot.you.id && player.initialReady === false);
  if (!isMyTurn && !canGift && !waitingForInitialReady) return null;
  let prompt = "";
  if (waitingForInitialReady) prompt = "Ready";
  if (isMyTurn && snapshot.phase === "await_draw") prompt = "Draw";
  if (isMyTurn && snapshot.phase === "await_choice") prompt = "Choose";
  if (isMyTurn && (snapshot.phase === "await_self_peek" || snapshot.phase === "await_opponent_peek" || snapshot.phase === "await_king_peek")) prompt = "Peek";
  if (isMyTurn && snapshot.phase === "await_swap") prompt = "Swap";
  if (canGift) prompt = "Give";
  if (!prompt) return null;
  return <div className="turn-prompt"><span className={isMyTurn || canGift || waitingForInitialReady ? "pulse" : ""} /><b>{prompt}</b><TurnCountdown deadlineAt={snapshot.deadlineAt} /></div>;
}

function TurnCountdown({ deadlineAt }: { deadlineAt?: number }) {
  const [seconds, setSeconds] = useState<number>();

  useEffect(() => {
    if (!deadlineAt) {
      setSeconds(undefined);
      return;
    }
    const update = () => setSeconds(Math.max(0, Math.ceil((deadlineAt - Date.now()) / 1000)));
    update();
    const timer = window.setInterval(update, 200);
    return () => window.clearInterval(timer);
  }, [deadlineAt]);

  if (seconds === undefined) return null;
  return <time className={`turn-countdown ${seconds <= 3 ? "urgent" : ""}`} aria-label={`${seconds} seconds remaining`}>{seconds}s</time>;
}

function PlayingCard({ card, compact = false, flipped = false }: { card: Card; compact?: boolean; flipped?: boolean }) {
  const Face = faceFor(card);
  return (
    <div className={`playing-card asset-card ${compact ? "compact" : ""} ${isRed(card) ? "red" : "black"} ${flipped ? "flip-in" : ""}`} aria-label={cardLabel(card)}>
      <Suspense fallback={<span className="card-face-loading" />}>
        <Face title={cardLabel(card)} width="100%" height="100%" />
      </Suspense>
    </div>
  );
}

function CardBack({ compact = false }: { compact?: boolean }) {
  return <div className={`card-back ${compact ? "compact" : ""}`} aria-label="Face-down card" />;
}

function PeekableCard({ card, compact = false }: { card?: Card; compact?: boolean }) {
  const [face, setFace] = useState<Card | undefined>(card);
  const [revealed, setRevealed] = useState(false);

  useEffect(() => {
    let firstFrame: number | undefined;
    let secondFrame: number | undefined;
    let hideTimer: number | undefined;
    if (card) {
      setFace(card);
      setRevealed(false);
      firstFrame = window.requestAnimationFrame(() => {
        secondFrame = window.requestAnimationFrame(() => setRevealed(true));
      });
    } else if (face) {
      setRevealed(false);
      hideTimer = window.setTimeout(() => setFace(undefined), 460);
    }
    return () => {
      if (firstFrame !== undefined) window.cancelAnimationFrame(firstFrame);
      if (secondFrame !== undefined) window.cancelAnimationFrame(secondFrame);
      if (hideTimer !== undefined) window.clearTimeout(hideTimer);
    };
  }, [card?.id]);

  if (!face) return <CardBack compact={compact} />;
  return (
    <div className={`peek-card ${compact ? "compact" : ""} ${revealed ? "revealed" : ""}`} aria-label={revealed ? cardLabel(face) : "Face-down card"}>
      <div className="peek-card-layer peek-card-back" aria-hidden="true"><CardBack compact={compact} /></div>
      <div className="peek-card-layer peek-card-front" aria-hidden="true"><PlayingCard card={face} compact={compact} /></div>
    </div>
  );
}

function sameRef(a: CardRef, b: CardRef) {
  return a.playerId === b.playerId && a.slot === b.slot;
}

function handVisualOrder(slot: number) {
  if (slot === 1) return 2;
  if (slot === 2) return 1;
  return slot;
}

function powerHint(snapshot: SnapshotMessage, target: CardRef, occupied: boolean): "peek" | "swap" | undefined {
  if (!occupied || snapshot.youRole !== "active" || snapshot.currentPlayerId !== snapshot.you.id) return undefined;
  const mine = target.playerId === snapshot.you.id;
  if (snapshot.phase === "await_self_peek" && mine) return "peek";
  if ((snapshot.phase === "await_opponent_peek" || snapshot.phase === "await_king_peek") && !mine) return "peek";
  if (snapshot.phase === "await_swap") return "swap";
  return undefined;
}

function refKey(ref: CardRef) {
  return `${ref.playerId}:${ref.slot}`;
}

function revealAt(snapshot: SnapshotMessage, target: CardRef): Card | undefined {
  return snapshot.reveal?.cards.find((item) => sameRef(item.target, target))?.card;
}

function rosterName(snapshot: SnapshotMessage, id: string): string | undefined {
  return [...snapshot.players, ...snapshot.nextRoundPlayers, ...snapshot.waitingPlayers].find((player) => player.id === id)?.name;
}

function uSeatStyle(index: number, total: number): CSSProperties {
  const spread = total <= 1 ? 0 : total === 2 ? 36 : total === 3 ? 64 : 84;
  const x = total <= 1 ? 50 : 50 - spread / 2 + (spread * index) / (total - 1);
  const edge = Math.abs(x - 50) / Math.max(spread / 2, 1);
  const y = 6 + 39 * edge ** 1.55;
  const scale = total <= 4 ? 1 : total <= 6 ? .82 : .7;
  return { "--u-x": `${x}%`, "--u-y": `${y}%`, "--u-scale": scale } as CSSProperties;
}

function captureActionGeometry(action: ActionView): ActionGeometry {
  return {
    first: action.first ? captureAnchor(action.first) : undefined,
    second: action.second ? captureAnchor(action.second) : undefined,
    target: action.target ? captureAnchor(action.target) : undefined,
    discard: captureElementAnchor(document.querySelector<HTMLElement>(".discard-wrap .playing-card, .discard-wrap .card-back, .discard-wrap .empty-discard")),
    drawn: captureElementAnchor(document.querySelector<HTMLElement>(".drawn-card-zone .playing-card, .drawn-card-zone .card-back")),
  };
}

function captureAnchor(target: CardRef): ActionAnchor | undefined {
  const slot = slotFor(target);
  if (!slot) return undefined;
  const element = slot.querySelector<HTMLElement>(".playing-card, .card-back") ?? slot;
  return captureElementAnchor(element);
}

function captureElementAnchor(element: HTMLElement | null): ActionAnchor | undefined {
  if (!element) return undefined;
  return { element: element.cloneNode(true) as HTMLElement, rect: element.getBoundingClientRect() };
}

async function animateAction(action: ActionView, geometry: ActionGeometry): Promise<void> {
  switch (action.kind) {
    case "wrong_slap":
      return animateWrongSlap(action, geometry);
    case "swap":
      if (geometry.first && geometry.second && action.first && action.second) {
        return animateSwap(action.first, action.second, geometry.first, geometry.second);
      }
      return;
    case "replace":
      if (geometry.target && geometry.discard && geometry.drawn && action.target) {
        return animateReplace(action, action.target, geometry.target, geometry.discard, geometry.drawn);
      }
      return;
    case "discard":
      if (geometry.discard && geometry.drawn) return animateDiscard(action, geometry.discard, geometry.drawn);
      return;
    case "slap":
      if (geometry.target && geometry.discard) return animateSlap(action, geometry.target, geometry.discard);
      return;
    case "gift":
      if (geometry.first && geometry.second && action.first && action.second) {
        return animateGift(action.second, geometry.first);
      }
      return;
  }
}

function slotFor(target: CardRef): HTMLElement | undefined {
  return [...document.querySelectorAll<HTMLElement>("[data-card-ref]")].find((slot) => slot.dataset.cardRef === refKey(target));
}

async function animateSwap(firstRef: CardRef, secondRef: CardRef, first: ActionAnchor, second: ActionAnchor): Promise<void> {
  const firstDestination = liveCardRect(firstRef);
  const secondDestination = liveCardRect(secondRef);
  if (!firstDestination || !secondDestination) return;
  const restore = hideCardButtons([firstRef, secondRef]);
  try {
    await Promise.all([
      flyCard(first.element, first.rect, secondDestination, -28, 0, 820),
      flyCard(second.element, second.rect, firstDestination, 28, 0, 820),
    ]);
  } finally {
    restore();
  }
}

async function animateReplace(action: ActionView, targetRef: CardRef, target: ActionAnchor, discard: ActionAnchor, drawn: ActionAnchor): Promise<void> {
  const targetDestination = liveCardRect(targetRef);
  const discardDestination = liveDiscardRect();
  if (!targetDestination || !discardDestination) return;
  const releaseDiscard = pinActionCard(discard);
  try {
    await Promise.all([
      // Keep the outgoing card covering the old discard until the incoming
      // card has also finished. Otherwise the old discard flashes back for a
      // few frames between the two independent animations.
      flyCard(target.element, target.rect, discardDestination, -30, 0, 855, action.card),
      flyCard(drawn.element, drawn.rect, targetDestination, 34, 95, 760, undefined, true),
    ]);
  } finally {
    releaseDiscard();
  }
}

async function animateDiscard(action: ActionView, discard: ActionAnchor, drawn: ActionAnchor): Promise<void> {
  const discardDestination = liveDiscardRect();
  if (!discardDestination) return;
  const releaseDiscard = pinActionCard(discard);
  try {
    await flyCard(drawn.element, drawn.rect, discardDestination, -22, 0, 620, action.card);
  } finally {
    releaseDiscard();
  }
}

async function animateSlap(action: ActionView, target: ActionAnchor, discard: ActionAnchor): Promise<void> {
  const discardDestination = liveDiscardRect();
  if (!discardDestination) return;
  const releaseDiscard = pinActionCard(discard);
  try {
    await flyCard(target.element, target.rect, discardDestination, -38, 0, 680, action.card);
  } finally {
    releaseDiscard();
  }
}

function animateWrongSlap(action: ActionView, geometry: ActionGeometry): Promise<void> {
  if (!action.target || !action.card) {
    return animateWrongSlapShake(action.actorId);
  }
  const discard = document.querySelector<HTMLElement>(".discard-wrap");
  const source = slotFor(action.target);
  if (!discard || (!geometry.target && !source)) {
    return animateWrongSlapShake(action.actorId);
  }

  const sourceCard = source?.querySelector<HTMLElement>(".playing-card, .card-back, .empty-slot");
  const from = geometry.target?.rect ?? sourceCard?.getBoundingClientRect() ?? source?.getBoundingClientRect();
  if (!from) {
    return animateWrongSlapShake(action.actorId);
  }
  const discardRect = discard.getBoundingClientRect();
  const width = from.width || 70;
  const height = from.height || width * 1.4;
  const rightSide = discardRect.right + 12;
  const leftSide = discardRect.left - width - 12;
  const finalLeft = rightSide + width <= window.innerWidth - 8
    ? rightSide
    : leftSide >= 8
      ? leftSide
      : Math.min(window.innerWidth - width - 8, discardRect.left + 14);
  const finalTop = Math.max(8, Math.min(window.innerHeight - height - 8, discardRect.top + 10));
  const ghost = document.createElement("div");
  ghost.className = "wrong-slap-flight";
  Object.assign(ghost.style, {
    left: `${from.left}px`,
    top: `${from.top}px`,
    width: `${width}px`,
    height: `${height}px`,
    visibility: "visible",
  });
  document.body.appendChild(ghost);
  const root = createRoot(ghost);
  flushSync(() => root.render(<PlayingCard card={action.card!} compact />));

  const dx = finalLeft - from.left;
  const dy = finalTop - from.top;
  const animation = ghost.animate([
    { transform: "translate3d(0,0,0) rotate(0deg) scale(.88)", opacity: 0 },
    { transform: `translate3d(${dx * .45}px, ${dy * .45 - 28}px, 0) rotate(-7deg) scale(1.04)`, opacity: 1, offset: .46 },
    { transform: `translate3d(${dx}px, ${dy}px, 0) rotate(-9deg) scale(.9)`, opacity: .92 },
  ], { duration: 720, easing: "cubic-bezier(.2,.78,.2,1)", fill: "both" });
  return new Promise<void>((resolve) => {
    let settled = false;
    let fallback: number | undefined;
    const finish = (shake: boolean) => {
      if (settled) return;
      settled = true;
      if (fallback !== undefined) window.clearTimeout(fallback);
      try {
        root.unmount();
      } finally {
        ghost.remove();
      }
      if (shake) void animateWrongSlapShake(action.actorId).then(resolve);
      else resolve();
    };
    const finishWithShake = () => finish(true);
    const cancel = () => finish(false);
    animation.addEventListener("finish", finishWithShake, { once: true });
    animation.addEventListener("cancel", cancel, { once: true });
    fallback = window.setTimeout(finishWithShake, 980);
  });
}

function animateWrongSlapShake(playerID: string): Promise<void> {
  const area = [...document.querySelectorAll<HTMLElement>(".player-area")].find((item) => item.dataset.playerId === playerID);
  if (!area) return Promise.resolve();
  const areaRect = area.getBoundingClientRect();
  area.querySelector<HTMLElement>(".penalty-card-pending")?.classList.add("penalty-arriving");
  const flash = document.createElement("div");
  flash.className = "slap-flash";
  flash.style.setProperty("--flash-x", `${areaRect.left + areaRect.width / 2}px`);
  flash.style.setProperty("--flash-y", `${areaRect.top + areaRect.height / 2}px`);
  document.body.appendChild(flash);
  const flashAnimation = flash.animate([{ opacity: 0 }, { opacity: 1, offset: .28 }, { opacity: 0 }], { duration: 520, easing: "ease-out" });
  const removeFlash = () => flash.remove();
  flashAnimation.addEventListener("finish", removeFlash, { once: true });
  flashAnimation.addEventListener("cancel", removeFlash, { once: true });
  window.setTimeout(removeFlash, 720);
  const baseTransform = getComputedStyle(area).transform;
  const withOffset = (x: number, rotation = 0) => `${baseTransform === "none" ? "" : `${baseTransform} `}translateX(${x}px) rotate(${rotation}deg)`;
  const shakeAnimation = area.animate([
    { transform: withOffset(0), filter: "drop-shadow(0 0 0 rgba(255,75,75,0))" },
    { transform: withOffset(-9, -1), filter: "drop-shadow(0 0 30px rgba(255,75,75,.95))" },
    { transform: withOffset(8, 1), filter: "drop-shadow(0 0 38px rgba(255,75,75,.9))" },
    { transform: withOffset(-6), filter: "drop-shadow(0 0 24px rgba(255,75,75,.65))" },
    { transform: withOffset(4) },
    { transform: withOffset(0), filter: "drop-shadow(0 0 0 rgba(255,75,75,0))" },
  ], { duration: 620, easing: "ease-out" });
  return new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      resolve();
    };
    shakeAnimation.addEventListener("finish", finish, { once: true });
    shakeAnimation.addEventListener("cancel", finish, { once: true });
    window.setTimeout(finish, 760);
  });
}

async function animateGift(targetRef: CardRef, source: ActionAnchor): Promise<void> {
  const destination = liveCardRect(targetRef);
  if (!destination) return;
  await flyCard(source.element, source.rect, destination, 28, 0, 720);
}

function flyCard(card: HTMLElement, from: DOMRect, to: DOMRect, arc: number, delay: number, duration: number, face?: Card, flipToBack = false): Promise<void> {
  const ghost = document.createElement("div");
  ghost.className = "action-card-ghost";
  let root: ReturnType<typeof createRoot> | undefined;
  let flipper: HTMLElement | undefined;
  if (flipToBack) {
    flipper = document.createElement("div");
    flipper.className = "action-card-flipper";
    const front = document.createElement("div");
    front.className = "action-card-layer action-card-front";
    const back = document.createElement("div");
    back.className = "action-card-layer action-card-back";
    if (face) {
      root = createRoot(front);
      flushSync(() => root!.render(<PlayingCard card={face} compact />));
    } else {
      const clone = card.cloneNode(true) as HTMLElement;
      clone.classList.remove("flip-in");
      front.appendChild(clone);
    }
    back.appendChild(Object.assign(document.createElement("div"), { className: "card-back compact" }));
    flipper.append(front, back);
    ghost.append(flipper);
  } else if (face) {
    root = createRoot(ghost);
    flushSync(() => root!.render(<PlayingCard card={face} compact />));
  } else {
    const clone = card.cloneNode(true) as HTMLElement;
    clone.classList.remove("flip-in");
    ghost.appendChild(clone);
  }
  Object.assign(ghost.style, {
    position: "fixed",
    left: `${from.left}px`,
    top: `${from.top}px`,
    width: `${from.width}px`,
    height: `${from.height}px`,
    margin: "0",
    zIndex: "100",
    pointerEvents: "none",
    transformOrigin: "top left",
  });
  document.body.appendChild(ghost);
  const dx = to.left - from.left;
  const dy = to.top - from.top;
  const midWidth = from.width + (to.width - from.width) * .52;
  const midHeight = from.height + (to.height - from.height) * .52;
  const animation = ghost.animate([
    { width: `${from.width}px`, height: `${from.height}px`, transform: "translate3d(0,0,0) rotate(0deg)", opacity: 1 },
    { width: `${midWidth}px`, height: `${midHeight}px`, transform: `translate3d(${dx * .5}px, ${dy * .5 + arc}px, 0) rotate(${arc > 0 ? 7 : -7}deg)`, opacity: 1, offset: .52 },
    { width: `${to.width}px`, height: `${to.height}px`, transform: `translate3d(${dx}px, ${dy}px, 0) rotate(0deg)`, opacity: 1 },
  ], { duration, delay, easing: "cubic-bezier(.2,.78,.2,1)", fill: "both" });
  if (flipToBack && flipper) {
    const flipDelay = delay + duration * .68;
    const flipDuration = Math.min(300, duration * .32);
    flipper.animate([
      { transform: "rotateY(0deg)" },
      { transform: "rotateY(180deg)" },
    ], { duration: flipDuration, delay: flipDelay, easing: "cubic-bezier(.45,.05,.55,.95)", fill: "both" });
  }
  return new Promise<void>((resolve) => {
    let settled = false;
    let fallback: number | undefined;
    const finish = () => {
      if (settled) return;
      settled = true;
      if (fallback !== undefined) window.clearTimeout(fallback);
      try {
        root?.unmount();
      } finally {
        ghost.remove();
      }
      resolve();
    };
    animation.addEventListener("finish", finish, { once: true });
    animation.addEventListener("cancel", finish, { once: true });
    fallback = window.setTimeout(finish, duration + delay + 180);
  });
}

function pinActionCard(anchor: ActionAnchor): () => void {
  const ghost = document.createElement("div");
  ghost.className = "action-card-static";
  ghost.appendChild(anchor.element.cloneNode(true));
  Object.assign(ghost.style, {
    left: `${anchor.rect.left}px`,
    top: `${anchor.rect.top}px`,
    width: `${anchor.rect.width}px`,
    height: `${anchor.rect.height}px`,
  });
  document.body.appendChild(ghost);
  return () => ghost.remove();
}

function liveCardRect(target: CardRef): DOMRect | undefined {
  return liveCardElement(target)?.getBoundingClientRect();
}

function liveCardElement(target: CardRef): HTMLElement | undefined {
  const slot = slotFor(target);
  return slot?.querySelector<HTMLElement>(".playing-card, .card-back, .empty-slot") ?? slot;
}

function liveDiscardElement(): HTMLElement | undefined {
  return document.querySelector<HTMLElement>(".discard-wrap .playing-card, .discard-wrap .card-back, .discard-wrap .empty-discard") ?? undefined;
}

function liveDiscardRect(): DOMRect | undefined {
  return liveDiscardElement()?.getBoundingClientRect();
}

function holdActionVisuals(action: ActionView, activePlayerID?: string): () => void {
  const elements: Array<HTMLElement | undefined> = [];
  switch (action.kind) {
    case "replace":
      if (action.target) elements.push(liveCardElement(action.target));
      elements.push(liveDiscardElement());
      break;
    case "discard":
    case "slap":
      elements.push(liveDiscardElement());
      break;
    case "swap":
      if (action.first) elements.push(liveCardElement(action.first));
      if (action.second) elements.push(liveCardElement(action.second));
      break;
    case "gift":
      if (action.second) elements.push(liveCardElement(action.second));
      break;
    case "wrong_slap":
      break;
  }
  const releaseElements = hideElements(elements);
  const activeArea = activePlayerID
    ? [...document.querySelectorAll<HTMLElement>(".player-area")].find((area) => area.dataset.playerId === activePlayerID)
    : undefined;
  const penaltySlot = action.kind === "wrong_slap"
    ? [...document.querySelectorAll<HTMLElement>(".player-area")]
      .find((area) => area.dataset.playerId === action.actorId)
      ?.querySelector<HTMLElement>(".penalty-card-pending")
    : undefined;
  activeArea?.classList.add("action-active-held");
  return () => {
    releaseElements();
    activeArea?.classList.remove("action-active-held");
    penaltySlot?.classList.remove("penalty-arriving");
  };
}

function hideElements(elements: Array<HTMLElement | undefined>): () => void {
  const visible = [...new Set(elements.filter((element): element is HTMLElement => Boolean(element)))];
  visible.forEach((element) => element.classList.add("action-visual-hidden"));
  return () => visible.forEach((element) => element.classList.remove("action-visual-hidden"));
}

function hideCardButtons(targets: CardRef[]): () => void {
  const buttons = targets
    .map((target) => slotFor(target)?.querySelector<HTMLElement>(".card-button"))
    .filter((button): button is HTMLElement => Boolean(button));
  buttons.forEach((button) => button.classList.add("action-origin-hidden"));
  return () => buttons.forEach((button) => button.classList.remove("action-origin-hidden"));
}

function hash(value: string) {
  return [...value].reduce((total, char) => total + char.charCodeAt(0), 0);
}

function endReason(reason?: string) {
  if (reason === "called_end") return "Someone called Kabo.";
  if (reason === "player_has_zero_cards") return "A player cleared every card.";
  if (reason === "draw_pile_exhausted") return "The draw pile ran out.";
  return "The table has settled.";
}

export default App;

function readableError(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  if (error && typeof error === "object") {
    const value = error as { message?: unknown; code?: unknown };
    const detail = [value.message, value.code].filter((part) => typeof part === "string" || typeof part === "number").join(" · ");
    if (detail) return `Kabo could not start: ${detail}`;
  }
  return "Kabo could not start inside Discord. Check the Activity configuration and try again.";
}
