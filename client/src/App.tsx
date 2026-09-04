import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
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
import { ActionSequence } from "./actionSequence";
import { initializePlatform, type PlatformSession, websocketURL } from "./platform";

type ConnectionState = "connecting" | "open" | "closed";
type HandLayout = "strip" | "grid";

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

type ActionVisualHold = {
  id: number;
  release: () => void;
};

type FlightSettle = {
  holdAtDestination?: number;
  beforeRemove?: () => void;
  destination?: () => DOMRect | undefined;
};

const DOUBLE_TAP_WINDOW = 360;
const HAND_LAYOUT_STORAGE_KEY = "kabo-hand-layout";
const BUILD_VERSION = (import.meta.env.VITE_BUILD_VERSION || "local").slice(0, 5);
const activeActionAnimations = new Set<Animation>();

function savedHandLayout(): HandLayout {
  try {
    return window.localStorage.getItem(HAND_LAYOUT_STORAGE_KEY) === "grid" ? "grid" : "strip";
  } catch {
    return "strip";
  }
}

function actionAnimate(element: Element, keyframes: Keyframe[] | PropertyIndexedKeyframes, options?: number | KeyframeAnimationOptions) {
  const animation = element.animate(keyframes, options);
  activeActionAnimations.add(animation);
  const forget = () => activeActionAnimations.delete(animation);
  animation.addEventListener("finish", forget, { once: true });
  animation.addEventListener("cancel", forget, { once: true });
  return animation;
}

function cancelActionAnimations() {
  [...activeActionAnimations].forEach((animation) => animation.cancel());
  activeActionAnimations.clear();
  document.querySelectorAll(".action-card-ghost, .action-card-static, .wrong-slap-flight, .late-slap-flight, .slap-flash")
    .forEach((element) => element.remove());
}

function App() {
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
  const actionVisualHold = useRef<ActionVisualHold | undefined>(undefined);

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
    actionVisualHold.current?.release();
    actionVisualHold.current = undefined;
    cancelActionAnimations();
    document.documentElement.classList.remove("action-in-flight");
    clearHandCompaction();
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

    const geometry = captureActionGeometry(queued.action);
    clearHandCompaction();
    const heldActivePlayerID = renderedSnapshot.current?.currentPlayerId;
    const epoch = playbackEpoch.current;
    document.documentElement.classList.add("action-in-flight");
    animationRunning.current = true;
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
        if (epoch !== playbackEpoch.current) return;
        const deferred = actionSequence.current.finish(queued.action.id);
        if (deferred) {
          renderSnapshot(deferred, true);
        }
        if (actionVisualHold.current?.id === queued.action.id) {
          actionVisualHold.current.release();
          actionVisualHold.current = undefined;
        }
        animationRunning.current = false;
        if (!actionSequence.current.hasQueued()) {
          document.documentElement.classList.remove("action-in-flight");
          clearHandCompaction();
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
    window.addEventListener("offline", onOffline);
    return () => {
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener("resize", onResize);
      window.removeEventListener("offline", onOffline);
      if (resizeFrame !== undefined) window.cancelAnimationFrame(resizeFrame);
    };
  }, []);

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

      <section className={`game-surface ${snapshot.phase !== "lobby" ? `table-layout hands-${handLayout}` : ""} ${showingAftermath ? "round-ended" : ""}`}>
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
                    handLayout={handLayout}
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
                      <div className="discard-card-frame">
                        {snapshot.discardTop ? <PlayingCard card={snapshot.discardTop} compact /> : <div className="empty-discard">Discard</div>}
                      </div>
                      <small>DISCARD</small>
                    </div>
                  </div>
                  <TurnPrompt snapshot={snapshot} isMyTurn={isMyTurn} />
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

      <PeekRevealSpotlight snapshot={snapshot} />

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

function PeekRevealSpotlight({ snapshot }: { snapshot: SnapshotMessage }) {
  if (!snapshot.reveal || snapshot.reveal.kind === "initial") return null;
  return (
    <div className="peek-reveal-overlay" role="status" aria-label="Revealed card">
      {snapshot.reveal.cards.map(({ target, card }) => {
        const owner = snapshot.players.find((player) => player.id === target.playerId);
        const ownerName = target.playerId === snapshot.you.id ? "Your" : `${owner?.name ?? "Player"}'s`;
        return (
          <div className="peek-reveal-spotlight" key={refKey(target)}>
            <PlayingCard card={card} compact flipped />
            <span>{ownerName} card {target.slot + 1}</span>
          </div>
        );
      })}
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
      if (tap.current?.timer) window.clearTimeout(tap.current.timer);
      tap.current = undefined;
      onCard(target);
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

  const hasPeekReveal = snapshot.reveal?.kind !== "initial" && Boolean(snapshot.reveal?.cards.some((item) => item.target.playerId === player.id));
  const penaltyPending = snapshot.phase !== "ended" && snapshot.action?.kind === "wrong_slap" && snapshot.action.actorId === player.id;
  const winner = revealEnded && snapshot.phase === "ended" && snapshot.winnerIds?.includes(player.id);
  const arrangedRows = handLayout === "strip"
    ? [[...player.cards].sort((a, b) => a.slot - b.slot)]
    : [0, 1].map((row) => player.cards
      .filter((slot) => handVisualRow(slot.slot) === row)
      .sort((a, b) => handVisualColumn(a.slot) - handVisualColumn(b.slot)));
  return (
    <section ref={area} data-player-id={player.id} style={style} className={`player-area ${mine ? "mine" : ""} ${active ? "active" : ""} ${!player.connected ? "disconnected" : ""} ${winner ? "winner" : ""} ${hasPeekReveal ? "has-peek-reveal" : ""} ${revealEnded && snapshot.phase === "ended" ? "cards-revealed" : ""} ${snapshot.phase === "await_choice" && mine ? "replace-mode" : ""}`}>
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
                        : initialRevealHere && revealed
                          ? <PlayingCard card={revealed} compact />
                          : <PeekableCard card={revealed} compact />
                      : <div className="empty-slot" />}
                  </button>
                  {isSelected && <span className="selection-order" aria-hidden="true">{selectionOrder}</span>}
                  {initialRevealHere && revealed && <span className="opening-card-marker">Card {slot.slot + 1}</span>}
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
          <span><b>Remember cards 3 &amp; 4</b><small>These faces are shown in their real positions.</small></span>
          <TurnCountdown deadlineAt={snapshot.deadlineAt} />
          <button className="primary-button" onClick={onInitialDone}>Ready</button>
        </div>
      )}
    </section>
  );
}

function TurnPrompt({ snapshot, isMyTurn }: { snapshot: SnapshotMessage; isMyTurn: boolean }) {
  const canGift = snapshot.phase === "await_gift" && snapshot.pendingGift?.slapperId === snapshot.you.id;
  let action = "";
  if (snapshot.phase === "await_draw") action = "Draw";
  if (snapshot.phase === "await_choice") action = "Choose";
  if (snapshot.phase === "await_self_peek" || snapshot.phase === "await_opponent_peek" || snapshot.phase === "await_king_peek") action = "Peek";
  if (snapshot.phase === "await_swap") action = isMyTurn ? "Select 2 cards — swap is automatic" : "Choosing 2 cards";
  if (snapshot.phase === "await_gift") action = "Give";
  if (!action) return null;
  const activeName = snapshot.players.find((player) => player.id === snapshot.currentPlayerId)?.name ?? "Player";
  let prompt = isMyTurn ? action : `${activeName} · ${action}`;
  if (canGift) prompt = "Give";
  return <div className="turn-prompt"><span className={isMyTurn || canGift ? "pulse" : ""} /><b>{prompt}</b><TurnCountdown deadlineAt={snapshot.deadlineAt} /></div>;
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
      <Face title={cardLabel(card)} width="100%" height="100%" />
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
      hideTimer = window.setTimeout(() => setFace(undefined), 300);
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
  const y = 20 + 25 * edge ** 1.55;
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
    case "late_slap":
      return animateSlapReveal(action, geometry, false);
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
  const distance = Math.hypot(secondDestination.left - first.rect.left, secondDestination.top - first.rect.top);
  const swapArc = Math.min(96, Math.max(48, distance * .16));
  const restoreCards = hideElements([liveCardElement(firstRef), liveCardElement(secondRef)]);
  let arrivals = 0;
  let restored = false;
  const revealUnderFlights = () => {
    arrivals += 1;
    if (arrivals < 2 || restored) return;
    restored = true;
    restoreCards();
  };
  try {
    await Promise.all([
      flyCard(cardBackElement(), first.rect, secondDestination, -swapArc, 0, 760, undefined, false, 0, "swap-card-flight", { holdAtDestination: 90, beforeRemove: revealUnderFlights, destination: () => liveCardRect(secondRef) }),
      flyCard(cardBackElement(), second.rect, firstDestination, swapArc, 0, 760, undefined, false, 0, "swap-card-flight", { holdAtDestination: 90, beforeRemove: revealUnderFlights, destination: () => liveCardRect(firstRef) }),
    ]);
  } finally {
    if (!restored) restoreCards();
  }
}

function cardBackElement(): HTMLElement {
  return Object.assign(document.createElement("div"), { className: "card-back compact" });
}

async function animateReplace(action: ActionView, targetRef: CardRef, target: ActionAnchor, discard: ActionAnchor, drawn: ActionAnchor): Promise<void> {
  const targetDestination = liveCardRect(targetRef);
  const discardDestination = liveDiscardRect();
  if (!targetDestination || !discardDestination) return;
  const releaseDiscard = pinActionCard(discard, liveDiscardRect);
  const observerAlreadySeesBack = drawn.element.matches(".card-back")
    || (Boolean(drawn.element.querySelector(".card-back")) && !drawn.element.querySelector(".playing-card"));
  try {
    await Promise.all([
      // Keep the outgoing card covering the old discard until the incoming
      // card has also finished. Otherwise the old discard flashes back for a
      // few frames between the two independent animations.
      flyCard(target.element, target.rect, discardDestination, -30, 0, 855, action.card, false, 7, "", { destination: liveDiscardRect }),
      flyCard(drawn.element, drawn.rect, targetDestination, 34, 95, 760, undefined, !observerAlreadySeesBack, 7, "", { destination: () => liveCardRect(targetRef) }),
    ]);
  } finally {
    releaseDiscard();
  }
}

async function animateDiscard(action: ActionView, discard: ActionAnchor, drawn: ActionAnchor): Promise<void> {
  const discardDestination = liveDiscardRect();
  if (!discardDestination) return;
  const releaseDiscard = pinActionCard(discard, liveDiscardRect);
  try {
    await flyCard(drawn.element, drawn.rect, discardDestination, -22, 0, 620, action.card, false, 7, "", { destination: liveDiscardRect });
  } finally {
    releaseDiscard();
  }
}

async function animateSlap(action: ActionView, target: ActionAnchor, discard: ActionAnchor): Promise<void> {
  const discardDestination = liveDiscardRect();
  if (!discardDestination) return;
  const releaseDiscard = pinActionCard(discard, liveDiscardRect);
  try {
    await flyCard(target.element, target.rect, discardDestination, -38, 0, 680, action.card, false, 7, "", { destination: liveDiscardRect });
    if (action.target) await animateHandCompaction(action.target.playerId);
  } finally {
    releaseDiscard();
  }
}

async function animateHandCompaction(playerId: string): Promise<void> {
  const grid = [...document.querySelectorAll<HTMLElement>(".player-area")]
    .find((area) => area.dataset.playerId === playerId)
    ?.querySelector<HTMLElement>(".card-grid");
  if (!grid) return;
  const cards = [...grid.querySelectorAll<HTMLElement>(".slot-wrap:not(.empty-slot-anchor)")];
  const before = new Map(cards.map((card) => [card.dataset.cardRef, card.getBoundingClientRect()]));
  grid.classList.add("hand-compacting");
  const animations = cards.flatMap((card) => {
    const previous = before.get(card.dataset.cardRef);
    if (!previous) return [];
    const next = card.getBoundingClientRect();
    const offsetX = previous.left - next.left;
    if (Math.abs(offsetX) < 1) return [];
    return [actionAnimate(card, [
      { transform: `translate3d(${offsetX}px, 0, 0)` },
      { transform: "translate3d(0, 0, 0)" },
    ], { duration: 340, easing: "cubic-bezier(.2,.75,.25,1)", fill: "both" })];
  });
  await Promise.all(animations.map((animation) => animation.finished.catch(() => undefined)));
}

function clearHandCompaction() {
  document.querySelectorAll(".card-grid.hand-compacting")
    .forEach((grid) => grid.classList.remove("hand-compacting"));
}

function animateWrongSlap(action: ActionView, geometry: ActionGeometry): Promise<void> {
  return animateSlapReveal(action, geometry, true);
}

function animateSlapReveal(action: ActionView, geometry: ActionGeometry, penalized: boolean): Promise<void> {
  if (!action.target || !action.card) {
    return penalized ? animateWrongSlapShake(action.actorId) : Promise.resolve();
  }
  const discard = document.querySelector<HTMLElement>(".discard-wrap");
  const source = slotFor(action.target);
  if (!discard || (!geometry.target && !source)) {
    return penalized ? animateWrongSlapShake(action.actorId) : Promise.resolve();
  }

  const sourceCard = source?.querySelector<HTMLElement>(".playing-card, .card-back, .empty-slot");
  const from = geometry.target?.rect ?? sourceCard?.getBoundingClientRect() ?? source?.getBoundingClientRect();
  if (!from) {
    return penalized ? animateWrongSlapShake(action.actorId) : Promise.resolve();
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
  ghost.className = penalized ? "wrong-slap-flight" : "late-slap-flight";
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
  const animation = actionAnimate(ghost, [
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
    const finishWithShake = () => finish(penalized);
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
  const flashAnimation = actionAnimate(flash, [{ opacity: 0 }, { opacity: 1, offset: .28 }, { opacity: 0 }], { duration: 520, easing: "ease-out" });
  const removeFlash = () => flash.remove();
  flashAnimation.addEventListener("finish", removeFlash, { once: true });
  flashAnimation.addEventListener("cancel", removeFlash, { once: true });
  window.setTimeout(removeFlash, 720);
  const baseTransform = getComputedStyle(area).transform;
  const withOffset = (x: number, rotation = 0) => `${baseTransform === "none" ? "" : `${baseTransform} `}translateX(${x}px) rotate(${rotation}deg)`;
  const shakeAnimation = actionAnimate(area, [
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
  await flyCard(source.element, source.rect, destination, 28, 0, 720, undefined, false, 7, "", { destination: () => liveCardRect(targetRef) });
}

function flyCard(
  card: HTMLElement,
  from: DOMRect,
  to: DOMRect,
  arc: number,
  delay: number,
  duration: number,
  face?: Card,
  flipToBack = false,
  tilt = 7,
  flightClass = "",
  settle?: FlightSettle,
): Promise<void> {
  const ghost = document.createElement("div");
  ghost.className = `action-card-ghost ${flightClass}`.trim();
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
  const animation = actionAnimate(ghost, [
    { width: `${from.width}px`, height: `${from.height}px`, transform: "translate3d(0,0,0) rotate(0deg)", opacity: 1 },
    { width: `${midWidth}px`, height: `${midHeight}px`, transform: `translate3d(${dx * .5}px, ${dy * .5 + arc}px, 0) rotate(${arc > 0 ? tilt : -tilt}deg)`, opacity: 1, offset: .52 },
    { width: `${to.width}px`, height: `${to.height}px`, transform: `translate3d(${dx}px, ${dy}px, 0) rotate(0deg)`, opacity: 1 },
  ], { duration, delay, easing: "cubic-bezier(.2,.78,.2,1)", fill: "both" });
  if (flipToBack && flipper) {
    const flipDelay = delay + duration * .68;
    const flipDuration = Math.min(300, duration * .32);
    actionAnimate(flipper, [
      { transform: "rotateY(0deg)" },
      { transform: "rotateY(180deg)" },
    ], { duration: flipDuration, delay: flipDelay, easing: "cubic-bezier(.45,.05,.55,.95)", fill: "both" });
  }
  return new Promise<void>((resolve) => {
    let settled = false;
    let fallback: number | undefined;
    const cleanup = () => {
      try {
        root?.unmount();
      } finally {
        ghost.remove();
      }
      resolve();
    };
    const finish = (arrived: boolean) => {
      if (settled) return;
      settled = true;
      if (fallback !== undefined) window.clearTimeout(fallback);
      if (!arrived) {
        cleanup();
        return;
      }
      void settleFlightGhost(ghost, settle?.destination).then(() => {
        if (!ghost.isConnected) {
          cleanup();
          return;
        }
        settle?.beforeRemove?.();
        const hold = settle?.holdAtDestination ?? 0;
        if (hold > 0) window.setTimeout(cleanup, hold);
        else cleanup();
      });
    };
    animation.addEventListener("finish", () => finish(true), { once: true });
    animation.addEventListener("cancel", () => finish(false), { once: true });
    fallback = window.setTimeout(() => finish(true), duration + delay + 180);
  });
}

async function settleFlightGhost(ghost: HTMLElement, destination?: () => DOMRect | undefined): Promise<void> {
  if (!destination || !ghost.isConnected) return;
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const target = destination();
    if (!target || !ghost.isConnected) return;
    const current = ghost.getBoundingClientRect();
    const dx = target.left - current.left;
    const dy = target.top - current.top;
    const dw = target.width - current.width;
    const dh = target.height - current.height;
    if (Math.max(Math.abs(dx), Math.abs(dy), Math.abs(dw), Math.abs(dh)) < .75) return;
    const computed = getComputedStyle(ghost);
    const left = Number.parseFloat(computed.left) || 0;
    const top = Number.parseFloat(computed.top) || 0;
    const correction = actionAnimate(ghost, [
      { left: `${left}px`, top: `${top}px`, width: `${current.width}px`, height: `${current.height}px` },
      { left: `${left + dx}px`, top: `${top + dy}px`, width: `${target.width}px`, height: `${target.height}px` },
    ], { duration: attempt === 0 ? 120 : 70, easing: "cubic-bezier(.2,.8,.2,1)", fill: "forwards" });
    await correction.finished.catch(() => undefined);
  }
}

function pinActionCard(anchor: ActionAnchor, destination?: () => DOMRect | undefined): () => void {
  const ghost = document.createElement("div");
  ghost.className = "action-card-static";
  ghost.appendChild(anchor.element.cloneNode(true));
  document.body.appendChild(ghost);
  let frame: number | undefined;
  const track = () => {
    const rect = destination?.() ?? anchor.rect;
    Object.assign(ghost.style, {
      left: `${rect.left}px`,
      top: `${rect.top}px`,
      width: `${rect.width}px`,
      height: `${rect.height}px`,
    });
    frame = window.requestAnimationFrame(track);
  };
  track();
  return () => {
    if (frame !== undefined) window.cancelAnimationFrame(frame);
    ghost.remove();
  };
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
      // Keep the destination backs rendered under the swap flights. Hiding
      // them caused a one-frame reveal after the ghosts landed.
      break;
    case "gift":
      if (action.second) elements.push(liveCardElement(action.second));
      break;
    case "wrong_slap":
      break;
    case "late_slap":
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
