import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
import { flushSync } from "react-dom";
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
import { ActionSequence } from "./actionSequence";
import { CardBack, PeekableCard, PlayingCard } from "./Card";
import { ActionMotionDirector } from "./motion/actionMotion";
import { initializePlatform, type PlatformSession, websocketURL } from "./platform";
import { useVisualViewport } from "./useVisualViewport";

type ConnectionState = "connecting" | "open" | "closed";
type HandLayout = "strip" | "grid";

const DOUBLE_TAP_WINDOW = 200;
const HAND_LAYOUT_STORAGE_KEY = "kabo-hand-layout";
const BUILD_VERSION = (import.meta.env.VITE_BUILD_VERSION || "local").slice(0, 5);

function savedHandLayout(): HandLayout {
  try {
    return window.localStorage.getItem(HAND_LAYOUT_STORAGE_KEY) === "strip" ? "strip" : "grid";
  } catch {
    return "grid";
  }
}

function App() {
  useVisualViewport();
  const [platform, setPlatform] = useState<PlatformSession>();
  const [snapshot, setSnapshot] = useState<SnapshotMessage>();
  const [connection, setConnection] = useState<ConnectionState>("connecting");
  const [problem, setProblem] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [swapSelection, setSwapSelection] = useState<CardRef[]>([]);
  const [actionAnimating, setActionAnimating] = useState(false);
  const [handLayout, setHandLayout] = useState<HandLayout>(savedHandLayout);
  const socket = useRef<WebSocket | undefined>(undefined);
  const renderedSnapshot = useRef<SnapshotMessage | undefined>(undefined);
  const latestSnapshot = useRef<SnapshotMessage | undefined>(undefined);
  const actionSequence = useRef(new ActionSequence<ActionView, SnapshotMessage>());
  const animationRunning = useRef(false);
  const playbackEpoch = useRef(0);
  const awaitingRecoverySnapshot = useRef(false);
  const animationPumpFrame = useRef<number | undefined>(undefined);
  const tableRoot = useRef<HTMLElement>(null);
  const actionMotion = useRef<ActionMotionDirector | undefined>(undefined);
  if (!actionMotion.current) actionMotion.current = new ActionMotionDirector(() => tableRoot.current);

  const renderSnapshot = (next: SnapshotMessage, synchronous = false) => {
    renderedSnapshot.current = next;
    if (synchronous) flushSync(() => setSnapshot(next));
    else setSnapshot(next);
  };

  const recoverVisualState = (authoritative?: SnapshotMessage) => {
    playbackEpoch.current += 1;
    actionSequence.current.recover(authoritative);
    animationRunning.current = false;
    if (animationPumpFrame.current !== undefined) {
      window.cancelAnimationFrame(animationPumpFrame.current);
      animationPumpFrame.current = undefined;
    }
    actionMotion.current?.cancel();
    if (authoritative) {
      renderedSnapshot.current = authoritative;
      flushSync(() => {
        setSnapshot(authoritative);
        setActionAnimating(false);
      });
    } else {
      setActionAnimating(false);
    }
  };

  const pumpActionQueue = () => {
    animationPumpFrame.current = undefined;
    if (animationRunning.current) return;
    const queued = actionSequence.current.beginNext();
    if (!queued) return;

    const motion = actionMotion.current!.capture(queued.action);
    const heldActivePlayerID = renderedSnapshot.current?.currentPlayerId;
    const epoch = playbackEpoch.current;
    animationRunning.current = true;
    renderedSnapshot.current = queued.snapshot;
    flushSync(() => {
      setActionAnimating(true);
      setSnapshot(queued.snapshot);
    });
    void actionMotion.current!.play(motion, {
      activePlayerId: heldActivePlayerID,
    })
      .catch((error: unknown) => console.error("Kabo action animation failed", error))
      .finally(() => {
        if (epoch !== playbackEpoch.current) return;
        const deferred = actionSequence.current.finish(queued.action.id);
        if (deferred) {
          renderSnapshot(deferred, true);
        }
        animationRunning.current = false;
        if (!actionSequence.current.hasQueued()) {
          flushSync(() => setActionAnimating(false));
        } else {
          scheduleActionPump();
        }
      });
  };

  const scheduleActionPump = () => {
    if (animationRunning.current || animationPumpFrame.current !== undefined || !actionSequence.current.hasQueued()) return;
    animationPumpFrame.current = window.requestAnimationFrame(pumpActionQueue);
  };

  const queueSnapshot = (next: SnapshotMessage) => {
    latestSnapshot.current = next;
    if (awaitingRecoverySnapshot.current) {
      awaitingRecoverySnapshot.current = false;
      recoverVisualState(next);
      setNotice("Back at the table");
      window.setTimeout(() => setNotice(undefined), 1800);
      return;
    }
    const decision = actionSequence.current.ingest(next, renderedSnapshot.current !== undefined);
    if (decision === "render") renderSnapshot(next);
    else if (decision === "queue") scheduleActionPump();
    else if (decision === "restart") recoverVisualState(next);
  };

  useEffect(() => {
    return () => actionMotion.current?.destroy();
  }, []);

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
          if (message.code === "wrong_slap" || message.message.startsWith("action is not available during")) return;
          setProblem(message.message);
          window.setTimeout(() => setProblem(undefined), 3200);
        } else {
          setNotice(message.message);
        }
      };
      ws.onclose = () => {
        if (stopped || socket.current !== ws) return;
        awaitingRecoverySnapshot.current = true;
        recoverVisualState(latestSnapshot.current);
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
    let resizeFrame: number | undefined;
    const recoverInterruptedLayout = () => {
      if (!animationRunning.current && !actionSequence.current.isBusy()) return;
      recoverVisualState(latestSnapshot.current);
    };
    const onVisibilityChange = () => {
      if (document.hidden) recoverInterruptedLayout();
    };
    const onResize = () => {
      if (resizeFrame !== undefined) window.cancelAnimationFrame(resizeFrame);
      resizeFrame = window.requestAnimationFrame(recoverInterruptedLayout);
    };
    const onOffline = () => socket.current?.close();
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("resize", onResize);
    window.visualViewport?.addEventListener("resize", onResize);
    window.visualViewport?.addEventListener("scroll", onResize);
    window.addEventListener("offline", onOffline);
    return () => {
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener("resize", onResize);
      window.visualViewport?.removeEventListener("resize", onResize);
      window.visualViewport?.removeEventListener("scroll", onResize);
      window.removeEventListener("offline", onOffline);
      if (resizeFrame !== undefined) window.cancelAnimationFrame(resizeFrame);
    };
  }, []);

  useEffect(() => {
    setSwapSelection([]);
  }, [snapshot?.phase, snapshot?.action?.id]);

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
    if (exists) {
      setSwapSelection(swapSelection.filter((item) => !sameRef(item, target)));
      return;
    }
    if (swapSelection.length === 0) {
      setSwapSelection([target]);
      return;
    }
    const first = swapSelection[0];
    setSwapSelection([first, target]);
    send({ type: "swap", first, second: target });
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

  const canDiscardDrawn = Boolean(connection === "open" && !actionAnimating && snapshot?.drawnCard && snapshot.phase === "await_choice" && snapshot.currentPlayerId === snapshot.you.id);
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
        <BuildStatus connection={connection} />
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
  const tableLocked = connection !== "open" || actionAnimating;
  const toggleHandLayout = () => {
    const next = handLayout === "strip" ? "grid" : "strip";
    setHandLayout(next);
    try {
      window.localStorage.setItem(HAND_LAYOUT_STORAGE_KEY, next);
    } catch {
      // The preference remains active for this session when storage is unavailable.
    }
  };

  return (
    <main className="app-shell">
      <header className="topbar">
        <div className="room-chip">Table <b>{platform.roomId.slice(-8)}</b></div>
        {!showingAftermath && <TurnPrompt snapshot={snapshot} isMyTurn={isMyTurn} />}
        <div className="topbar-actions">
          {snapshot.phase !== "lobby" && (
            <button
              className="ghost-button hand-layout-toggle"
              onClick={toggleHandLayout}
              disabled={actionAnimating}
              aria-label={`Switch to ${handLayout === "strip" ? "grid" : "strip"} hand layout`}
              title={`Hand layout: ${handLayout === "strip" ? "single-row strip" : "two-row grid"}`}
            >
              {handLayout === "strip" ? "1×N" : "2×N"}
            </button>
          )}
          {snapshot.phase !== "lobby" && snapshot.phase !== "ended" && <NextRoundRoster snapshot={snapshot} send={send} />}
          {platform.mode === "browser" && <button className="ghost-button" onClick={copyInvite}>Invite</button>}
        </div>
      </header>

      <section ref={tableRoot} className={`game-surface ${snapshot.phase !== "lobby" ? `table-layout hands-${handLayout}` : ""} ${showingAftermath ? "round-ended" : ""}`}>
        {snapshot.phase === "lobby" ? (
          <Lobby snapshot={snapshot} platform={platform} send={send} />
        ) : (
          <>
            {!showingAftermath && (
              <div className={`opponents opponents-u opponents-${opponents.length}`} aria-label="Opponents">
                {opponents.map((player, index) => (
                  <PlayerArea
                    key={player.id}
                    style={uSeatStyle(index, opponents.length)}
                    player={player}
                    handLayout="grid"
                    snapshot={snapshot}
                    selected={swapSelection}
                    onCard={cardAction}
                    onSlap={slap}
                    onGift={(slot) => send({ type: "gift", sourceSlot: slot })}
                    canInteract={!tableLocked && !isSpectator && snapshot.phase !== "ended"}
                    revealEnded={showingAftermath}
                  />
                ))}
              </div>
            )}

            <div className={`table-center slap-dropzone ${showingAftermath ? "aftermath-center" : ""}`} onDragOver={(event) => event.preventDefault()} onDrop={dropToSlap}>
              {showingAftermath ? (
                <RoundSummary snapshot={snapshot} winners={winnerNames} losers={loserNames} send={send} canStart={!isSpectator} />
              ) : (
                <>
                  <div className="pile-zone">
                    <button className="deck" disabled={tableLocked || !isMyTurn || snapshot.phase !== "await_draw"} onClick={() => send({ type: "draw" })}>
                      <span>{snapshot.drawPileCount}</span>
                      <small>DRAW</small>
                    </button>
                    <div className={`drawn-card-position ${snapshot.hasDrawnCard ? "occupied" : ""}`} data-motion-zone="drawn">
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
                      <div className="discard-card-frame" data-motion-zone="discard">
                        {snapshot.discardTop ? <PlayingCard card={snapshot.discardTop} compact /> : <div className="empty-discard">Discard</div>}
                      </div>
                      <small>DISCARD</small>
                    </div>
                  </div>
                </>
              )}
            </div>

            {me && !showingAftermath && (
              <div className="my-area">
                <PlayerArea player={me} handLayout={handLayout} snapshot={snapshot} selected={swapSelection} onCard={cardAction} onSlap={slap} onGift={(slot) => send({ type: "gift", sourceSlot: slot })} onInitialDone={() => send({ type: "acknowledge_initial" })} mine canInteract={!tableLocked && snapshot.phase !== "ended"} revealEnded={showingAftermath} />
                {isMyTurn && snapshot.phase === "await_draw" && (
                  <button className="kabo-button" disabled={tableLocked} onClick={() => send({ type: "call_end" })}>KABO!</button>
                )}
              </div>
            )}
          </>
        )}
      </section>

      {connection !== "open" && (
        <div className="reconnect-banner" role="status" aria-live="polite">
          <span className={`connection-dot ${connection}`} />
          Reconnecting · actions paused
        </div>
      )}
      {problem && <div className="toast error" role="alert">{problem}</div>}
      {notice && <div className="toast">{notice}</div>}
      <BuildStatus connection={connection} />
    </main>
  );
}

function BuildStatus({ connection }: { connection: ConnectionState }) {
  return (
    <div className="build-status" title={`Build ${BUILD_VERSION}`}>
      <code>{BUILD_VERSION}</code>
      <span className={`connection-dot ${connection}`} aria-label={connection === "open" ? "Connected" : "Reconnecting"} />
    </div>
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
  const waitingNames = players.filter((player) => !player.ready).map((player) => player.id === snapshot.you.id ? "you" : player.name);
  return (
    <div className="round-summary next-round-lobby">
      <span className="eyebrow">ROUND COMPLETE</span>
      <strong>{winners.join(" & ")} {winners.length === 1 ? "wins" : "tie"}</strong>
      {losers.length > 0 && <small className="loser-note">Loser · {losers.join(" & ")}</small>}
      <small>{endReason(snapshot.endReason)}</small>
      {starter && <small className="starter-note">{starter} starts</small>}
      <div className="final-hands" aria-label="Everyone's final cards">
        {snapshot.players.map((player) => (
          <section className={snapshot.winnerIds?.includes(player.id) ? "winner" : ""} key={player.id}>
            <div className="final-hand-heading">
              <b>{player.id === snapshot.you.id ? "You" : player.name}</b>
              <span>{player.score} pts</span>
            </div>
            <div className="final-hand-cards">
              {player.cards.filter((slot) => slot.card).map((slot) => <PlayingCard key={slot.slot} card={slot.card!} compact />)}
            </div>
          </section>
        ))}
      </div>
      {canJoin && <button className="join-next-round summary-join" onClick={() => send({ type: "set_next_round", joinNextRound: true })}>Join</button>}
      {players.length > 0 && (
        <>
          <div className="ready-summary"><strong>{readyCount}/{players.length}</strong><span>ready</span></div>
          <ReadyRoster snapshot={snapshot} send={send} />
        </>
      )}
      {canStart ? <button className="primary-button" disabled={!snapshot.allReady} onClick={() => send({ type: "start_game" })}>{snapshot.allReady ? "Play next round" : `Waiting for ${waitingNames.join(" & ")}`}</button> : <small>Waiting for a player to start.</small>}
    </div>
  );
}

function PlayerArea({ player, handLayout, snapshot, selected, onCard, onSlap, onGift, onInitialDone, style, mine = false, canInteract = true, revealEnded = true }: {
  player: PlayerView;
  handLayout: HandLayout;
  snapshot: SnapshotMessage;
  selected: CardRef[];
  onCard: (target: CardRef) => void;
  onSlap: (target: CardRef) => void;
  onGift: (slot: number) => void;
  onInitialDone?: () => void;
  style?: CSSProperties;
  mine?: boolean;
  canInteract?: boolean;
  revealEnded?: boolean;
}) {
  const active = snapshot.currentPlayerId === player.id && snapshot.phase !== "initial_peek" && snapshot.phase !== "ended";
  const initialRevealHere = mine && snapshot.reveal?.kind === "initial";
  const stillLooking = snapshot.phase === "initial_peek" && player.initialReady === false;
  const drawing = active && snapshot.phase === "await_choice";
  const giftMode = canInteract && snapshot.phase === "await_gift" && snapshot.pendingGift?.slapperId === snapshot.you.id && player.id === snapshot.you.id;
  const tap = useRef<{ key: string; at: number; timer?: number } | undefined>(undefined);
  const pendingTap = useRef<{ element: HTMLElement; timer: number } | undefined>(undefined);
  const dragging = useRef(false);
  const area = useRef<HTMLElement>(null);

  const clearPendingTap = () => {
    if (!pendingTap.current) return;
    window.clearTimeout(pendingTap.current.timer);
    pendingTap.current.element.classList.remove("tap-pending");
    pendingTap.current = undefined;
  };

  useEffect(() => () => {
    if (tap.current?.timer) window.clearTimeout(tap.current.timer);
    clearPendingTap();
  }, []);

  useEffect(() => {
    if (tap.current?.timer) window.clearTimeout(tap.current.timer);
    tap.current = undefined;
    clearPendingTap();
  }, [snapshot.phase, snapshot.discardEventId, snapshot.action?.id]);

  const handleTap = (target: CardRef, element?: HTMLElement) => {
    if (dragging.current || !canInteract) return;
    const key = refKey(target);
    const now = Date.now();
    if (tap.current?.key === key && now - tap.current.at <= DOUBLE_TAP_WINDOW) {
      if (tap.current.timer) window.clearTimeout(tap.current.timer);
      tap.current = undefined;
      clearPendingTap();
      onSlap(target);
      return;
    }
    if (tap.current?.timer) window.clearTimeout(tap.current.timer);
    clearPendingTap();
    if (snapshot.phase === "await_swap" && snapshot.currentPlayerId === snapshot.you.id) {
      const timer = window.setTimeout(() => {
        if (tap.current?.key === key && tap.current.at === now) tap.current = undefined;
      }, DOUBLE_TAP_WINDOW);
      tap.current = { key, at: now, timer };
      onCard(target);
      return;
    }
    if (element) {
      element.classList.add("tap-pending");
      pendingTap.current = {
        element,
        timer: window.setTimeout(clearPendingTap, DOUBLE_TAP_WINDOW + 1_500),
      };
    }
    const timer = window.setTimeout(() => {
      if (dragging.current) {
        tap.current = undefined;
        clearPendingTap();
        return;
      }
      if (giftMode) onGift(target.slot);
      else onCard(target);
      tap.current = undefined;
    }, DOUBLE_TAP_WINDOW);
    tap.current = { key, at: now, timer };
  };

  const penaltyPending = snapshot.phase !== "ended" && snapshot.action?.kind === "wrong_slap" && snapshot.action.actorId === player.id;
  const winner = revealEnded && snapshot.phase === "ended" && snapshot.winnerIds?.includes(player.id);
  const arrangedRows = handLayout === "strip"
    ? [[...player.cards].sort((a, b) => a.slot - b.slot)]
    : [0, 1].map((row) => player.cards
      .filter((slot) => handVisualRow(slot.slot) === row)
      .sort((a, b) => handVisualColumn(a.slot) - handVisualColumn(b.slot)));
  return (
    <section ref={area} data-player-id={player.id} style={style} className={`player-area ${mine ? "mine" : ""} ${active ? "active" : ""} ${!player.connected ? "disconnected" : ""} ${winner ? "winner" : ""} ${revealEnded && snapshot.phase === "ended" ? "cards-revealed" : ""} ${snapshot.phase === "await_choice" && mine ? "replace-mode" : ""}`}>
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
        {arrangedRows.map((row, rowIndex) => (
          <div className="card-row" data-hand-row={rowIndex} key={rowIndex}>
            {row.map((slot) => {
              const target = { playerId: player.id, slot: slot.slot };
              const selectionOrder = selected.findIndex((item) => sameRef(item, target)) + 1;
              const isSelected = selectionOrder > 0;
              const revealed = revealAt(snapshot, target);
              const peekReveal = Boolean(revealed && snapshot.reveal?.kind !== "initial");
              const power = canInteract ? powerHint(snapshot, target, slot.occupied) : undefined;
              const peekedByOther = snapshot.publicPeek?.viewerId !== snapshot.you.id && snapshot.publicPeek && sameRef(snapshot.publicPeek.target, target);
              const vacatedDuringAction = !slot.occupied && (
                (snapshot.action?.kind === "slap" && snapshot.action.target && sameRef(snapshot.action.target, target))
                || (snapshot.action?.kind === "gift" && snapshot.action.first && sameRef(snapshot.action.first, target))
              );
              return (
                <div className={`slot-wrap ${!slot.occupied ? "empty-slot-anchor" : ""} ${vacatedDuringAction ? "action-vacated" : ""} ${penaltyPending && slot.slot === player.cards.length - 1 ? "penalty-card-pending" : ""} ${isSelected ? "selected" : ""} ${revealed ? "is-revealed" : ""} ${peekReveal ? "peek-reveal" : ""} ${peekedByOther ? "peek-observed" : ""} ${power ? `power-target power-${power}` : ""}`} key={slot.slot} data-card-ref={refKey(target)}>
                  <button
                    className="card-button"
                    disabled={!slot.occupied || !canInteract}
                    draggable={slot.occupied && canInteract}
                    aria-label={`${mine ? "Your" : player.name}'s card ${slot.slot + 1}${power ? ` · ${power === "peek" ? "peek target" : "swap target"}` : ""}`}
                    onClick={(event) => { if (event.detail === 0) giftMode ? onGift(slot.slot) : onCard(target); }}
                    onPointerDown={(event) => {
                      if (event.isPrimary && event.button === 0) handleTap(target, event.currentTarget.closest<HTMLElement>(".slot-wrap") ?? undefined);
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
                        : initialRevealHere && revealed
                          ? <PlayingCard card={revealed} compact />
                          : <PeekableCard card={revealed} compact />
                      : <div className="empty-slot" />}
                  </button>
                  {isSelected && <span className="selection-order" aria-hidden="true">{selectionOrder}</span>}
                  {giftMode && slot.occupied && (
                    <button className="gift-button" onClick={() => onGift(slot.slot)}>GIVE</button>
                  )}
                  {revealed && snapshot.reveal?.kind !== "initial" && <span className="peek-timer" aria-label="Hides in three seconds" />}
                  {peekedByOther && <span className="peek-indicator" role="status" aria-label={`${snapshot.players.find((item) => item.id === snapshot.publicPeek?.viewerId)?.name ?? "A player"} is peeking at this card`}><i /></span>}
                </div>
              );
            })}
          </div>
        ))}
      </div>
      {initialRevealHere && onInitialDone && (
        <div className="opening-guide" role="status">
          <b>Memorize</b>
          <TurnCountdown deadlineAt={snapshot.deadlineAt} />
          <button className="primary-button" onClick={onInitialDone}>Ready</button>
        </div>
      )}
    </section>
  );
}

function TurnPrompt({ snapshot, isMyTurn }: { snapshot: SnapshotMessage; isMyTurn: boolean }) {
  let action = "";
  if (snapshot.phase === "await_draw") action = "Draw";
  if (snapshot.phase === "await_choice") action = "Choose";
  if (snapshot.phase === "await_self_peek" || snapshot.phase === "await_opponent_peek" || snapshot.phase === "await_king_peek") action = "Peek";
  if (snapshot.phase === "await_swap") action = isMyTurn ? "Select 2 to swap" : "Choosing cards";
  if (snapshot.phase === "await_gift") action = "Give";
  if (!action) return null;
  return <><TurnProgress deadlineAt={snapshot.deadlineAt} /><div className="turn-prompt"><b>{action}</b></div></>;
}

function TurnProgress({ deadlineAt }: { deadlineAt?: number }) {
  const [remainingMs, setRemainingMs] = useState<number>();

  useEffect(() => {
    if (!deadlineAt) {
      setRemainingMs(undefined);
      return;
    }
    const update = () => setRemainingMs(Math.max(0, deadlineAt - Date.now()));
    update();
    const timer = window.setInterval(update, 100);
    return () => window.clearInterval(timer);
  }, [deadlineAt]);

  if (remainingMs === undefined) return null;
  const progress = Math.min(1, remainingMs / 15_000);
  const hue = progress > .5 ? 42 + (progress - .5) * 200 : 4 + progress * 76;
  const color = `hsl(${hue} 78% 57%)`;
  const seconds = Math.ceil(remainingMs / 1_000);
  return (
    <time className="turn-progress" aria-label={`${seconds} seconds remaining`}>
      <span style={{ transform: `scaleX(${progress})`, backgroundColor: color, color }} />
    </time>
  );
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

function sameRef(a: CardRef, b: CardRef) {
  return a.playerId === b.playerId && a.slot === b.slot;
}

function handVisualRow(slot: number) {
  if (slot < 2) return 0;
  if (slot < 4) return 1;
  return (slot - 4) % 2;
}

function handVisualColumn(slot: number) {
  if (slot < 2) return slot;
  if (slot < 4) return slot - 2;
  return 2 + Math.floor((slot - 4) / 2);
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
  // Four or more wide hands need a larger edge gutter than their center
  // points suggest, especially inside Discord's high-DPI activity panels.
  const spread = total <= 1 ? 0 : total === 2 ? 36 : total === 3 ? 64 : 72;
  const x = total <= 1 ? 50 : 50 - spread / 2 + (spread * index) / (total - 1);
  const edge = Math.abs(x - 50) / Math.max(spread / 2, 1);
  const y = 25 + 27 * edge ** 1.55;
  const scale = total <= 4 ? 1 : total <= 6 ? .82 : .7;
  return { "--u-x": `${x}%`, "--u-y": `${y}%`, "--u-scale": scale } as CSSProperties;
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
