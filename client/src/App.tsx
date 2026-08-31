import { Suspense, useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
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

function App() {
  const [platform, setPlatform] = useState<PlatformSession>();
  const [snapshot, setSnapshot] = useState<SnapshotMessage>();
  const [connection, setConnection] = useState<ConnectionState>("connecting");
  const [problem, setProblem] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [swapSelection, setSwapSelection] = useState<CardRef[]>([]);
  const [wrongSlapTick, setWrongSlapTick] = useState(0);
  const socket = useRef<WebSocket | undefined>(undefined);
  const lastActionID = useRef(0);

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
          setSnapshot(message);
          return;
        }
        if (message.type === "error") {
          if (message.code === "wrong_slap") {
            setWrongSlapTick((tick) => tick + 1);
            return;
          }
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

  useEffect(() => {
    const action = snapshot?.action;
    if (!action || action.id === lastActionID.current) return;
    lastActionID.current = action.id;
    const frame = window.requestAnimationFrame(() => animateAction(action));
    return () => window.cancelAnimationFrame(frame);
  }, [snapshot?.action?.id]);

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
    const timer = window.setTimeout(() => send({ type: "acknowledge_reveal" }), 3000);
    return () => window.clearTimeout(timer);
  }, [send, revealKey]);

  const chooseSwap = (target: CardRef) => {
    if (!snapshot || snapshot.phase !== "await_swap" || snapshot.currentPlayerId !== snapshot.you.id) return;
    const exists = swapSelection.some((item) => sameRef(item, target));
    const next = exists ? swapSelection.filter((item) => !sameRef(item, target)) : [...swapSelection, target].slice(-2);
    setSwapSelection(next);
    if (next.length === 2) {
      send({ type: "swap", first: next[0], second: next[1] });
    }
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

  return (
    <main className="app-shell">
      <header className="topbar">
        <div className="room-chip">Table <b>{platform.roomId.slice(-8)}</b></div>
        {platform.mode === "browser" && <button className="ghost-button" onClick={copyInvite}>Invite</button>}
      </header>

      <NextRoundRoster snapshot={snapshot} send={send} />

      <section className={`game-surface ${snapshot.phase !== "lobby" ? "table-layout" : ""} ${snapshot.phase === "ended" ? "round-ended" : ""}`}>
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
                  canInteract={!isSpectator}
                />
              ))}
            </div>

            <div className="table-center slap-dropzone" onDragOver={(event) => event.preventDefault()} onDrop={dropToSlap}>
              <div className={`pile-zone ${snapshot.phase === "ended" ? "settled" : ""}`}>
                <button className="deck" disabled={snapshot.phase === "ended" || !isMyTurn || snapshot.phase !== "await_draw"} onClick={() => send({ type: "draw" })}>
                  <span>{snapshot.drawPileCount}</span>
                  <small>{snapshot.phase === "ended" ? "LEFT" : "DRAW"}</small>
                </button>
                <div className={`drawn-card-position ${snapshot.hasDrawnCard ? "occupied" : ""}`}>
                  {snapshot.phase !== "ended" && snapshot.hasDrawnCard && (
                    <div className="drawn-card-zone">
                      {snapshot.drawnCard ? <PlayingCard card={snapshot.drawnCard} compact /> : <CardBack compact />}
                      {snapshot.drawnCard && (
                        <button className="discard-action" onClick={() => send({ type: "discard_drawn" })} title={`Discard ${cardLabel(snapshot.drawnCard)}`}>
                          DISCARD
                        </button>
                      )}
                    </div>
                  )}
                </div>
                <div className="discard-wrap">
                  {snapshot.discardTop ? <PlayingCard card={snapshot.discardTop} compact /> : <div className="empty-discard">Discard</div>}
                  <small>DISCARD</small>
                </div>
              </div>
              {snapshot.phase === "ended"
                ? <RoundSummary snapshot={snapshot} winners={winnerNames} send={send} canStart={!isSpectator} />
                : <>
                  <TurnPrompt snapshot={snapshot} isMyTurn={isMyTurn} />
                  {isSpectator && <SpectatorNotice joiningNextRound={snapshot.nextRoundJoined} />}
                </>}
            </div>

            {me && (
              <div className="my-area">
                <PlayerArea player={me} snapshot={snapshot} selected={swapSelection} onCard={cardAction} onSlap={slap} onGift={(slot) => send({ type: "gift", sourceSlot: slot })} onInitialDone={() => send({ type: "acknowledge_initial" })} wrongSlapTick={wrongSlapTick} mine canInteract />
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
  const spectators = snapshot.waitingPlayers.filter((player) => !player.joiningNextRound);
  const canToggle = snapshot.phase !== "lobby";
  const disabled = !canToggle || (!snapshot.nextRoundJoined && snapshot.nextRoundFull);
  const label = snapshot.nextRoundJoined ? "✓ Joining next round" : "Join next round";

  return (
    <aside className="next-round-panel" aria-label="Next round roster">
      <div className="next-round-heading">
        <div>
          <span className="eyebrow">NEXT ROUND</span>
          <strong>{snapshot.nextRoundPlayers.length} / 8</strong>
        </div>
        <span className="next-round-status">{snapshot.phase === "lobby" ? "Lobby" : "Queue"}</span>
      </div>
      <div className="next-round-list">
        {snapshot.nextRoundPlayers.map((player, index) => (
          <RosterRow key={player.id} player={player} index={index} />
        ))}
        {snapshot.nextRoundPlayers.length === 0 && <div className="next-round-empty">No players queued yet</div>}
      </div>
      <div className="next-round-open">
        {snapshot.nextRoundPlayers.length < 8 ? `${8 - snapshot.nextRoundPlayers.length} seat${snapshot.nextRoundPlayers.length === 7 ? "" : "s"} open` : "Roster full"}
      </div>
      {canToggle ? (
        <button
          className={`join-next-round ${snapshot.nextRoundJoined ? "joined" : ""}`}
          disabled={disabled}
          aria-pressed={snapshot.nextRoundJoined}
          onClick={() => send({ type: "set_next_round", joinNextRound: !snapshot.nextRoundJoined })}
        >
          {label}
        </button>
      ) : (
        <small className="next-round-note">Everyone here is in the current lobby.</small>
      )}
      {snapshot.phase !== "lobby" && snapshot.youRole === "spectator" && (
        <small className="next-round-note">{snapshot.nextRoundJoined ? "You are watching and queued." : "You are watching only."}</small>
      )}
      {spectators.length > 0 && (
        <div className="spectator-list">
          <span>Watching</span>
          <b>{spectators.map((player) => player.name).join(", ")}</b>
        </div>
      )}
    </aside>
  );
}

function RosterRow({ player, index }: { player: RosterPlayerView; index: number }) {
  return (
    <div className="next-round-player">
      <span className="roster-number">{index + 1}</span>
      <span className={`avatar avatar-${hash(player.id) % 5}`}>{player.name.slice(0, 1).toUpperCase()}</span>
      <b>{player.name}</b>
      <small className={player.connected ? "connected" : "away"}>{player.connected ? "Ready" : "Away"}</small>
    </div>
  );
}

function SpectatorNotice({ joiningNextRound }: { joiningNextRound: boolean }) {
  return <div className="spectator-notice"><span className="spectator-eye">◉</span> Spectating this round · {joiningNextRound ? "joining the next one" : "watching only"}</div>;
}

function Lobby({ snapshot, platform, send }: { snapshot: SnapshotMessage; platform: PlatformSession; send: (message: ClientMessage) => void }) {
  const canStart = snapshot.youRole === "active";
  const players = snapshot.nextRoundPlayers;
  return (
    <div className="lobby-card">
      <h1 className="lobby-title">KABO</h1>
      <div className="lobby-players">
        {players.map((player, index) => (
          <div className="lobby-player" key={player.id}><span>{index + 1}</span><b>{player.name}</b><em>{player.connected ? "Ready" : "Away"}</em></div>
        ))}
        {players.length < 2 && <div className="waiting-seat">Waiting for another player…</div>}
      </div>
      <button className="primary-button wide" disabled={!canStart || players.length < 2} onClick={() => send({ type: "start_game" })}>
        {!canStart ? "Waiting for a seat" : players.length < 2 ? "Need 2 players" : "Deal the cards"}
      </button>
      <small>{platform.mode === "discord" ? `${platform.participants ?? snapshot.players.length} in this Activity` : "Open the invite in another browser profile to join."}</small>
    </div>
  );
}

function RoundSummary({ snapshot, winners, send, canStart }: { snapshot: SnapshotMessage; winners: string[]; send: (message: ClientMessage) => void; canStart: boolean }) {
  return (
    <div className="round-summary">
      <span className="eyebrow">ROUND COMPLETE</span>
      <strong>{winners.join(" & ")} {winners.length === 1 ? "wins" : "tie"}</strong>
      <small>{endReason(snapshot.endReason)}</small>
      {canStart ? <button className="primary-button" onClick={() => send({ type: "start_game" })}>Next round</button> : <small>Waiting for an active player to start the next round.</small>}
    </div>
  );
}

function PlayerArea({ player, snapshot, selected, onCard, onSlap, onGift, onInitialDone, wrongSlapTick = 0, style, mine = false, canInteract = true }: {
  player: PlayerView;
  snapshot: SnapshotMessage;
  selected: CardRef[];
  onCard: (target: CardRef) => void;
  onSlap: (target: CardRef) => void;
  onGift: (slot: number) => void;
  onInitialDone: () => void;
  wrongSlapTick?: number;
  style?: CSSProperties;
  mine?: boolean;
  canInteract?: boolean;
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
    if (!mine || wrongSlapTick === 0 || !area.current) return;
    area.current.animate([
      { transform: "translateX(0)", filter: "drop-shadow(0 0 0 rgba(255,75,75,0))" },
      { transform: "translateX(-9px) rotate(-1deg)", filter: "drop-shadow(0 0 24px rgba(255,75,75,.95))", background: "rgba(255,75,75,.18)" },
      { transform: "translateX(8px) rotate(1deg)", filter: "drop-shadow(0 0 30px rgba(255,75,75,.9))", background: "rgba(255,75,75,.22)" },
      { transform: "translateX(-6px)", filter: "drop-shadow(0 0 18px rgba(255,75,75,.65))" },
      { transform: "translateX(4px)" },
      { transform: "translateX(0)", filter: "drop-shadow(0 0 0 rgba(255,75,75,0))", background: "transparent" },
    ], { duration: 620, easing: "ease-out" });
  }, [mine, wrongSlapTick]);

  const handleTap = (target: CardRef) => {
    if (dragging.current || !canInteract) return;
    const key = refKey(target);
    const now = Date.now();
    if (tap.current?.key === key && now - tap.current.at < 320) {
      if (tap.current.timer) window.clearTimeout(tap.current.timer);
      tap.current = undefined;
      onSlap(target);
      return;
    }
    if (tap.current?.timer) window.clearTimeout(tap.current.timer);
    const timer = window.setTimeout(() => {
      if (giftMode) onGift(target.slot);
      else onCard(target);
      tap.current = undefined;
    }, 260);
    tap.current = { key, at: now, timer };
  };

  const initialRevealHere = mine && snapshot.reveal?.kind === "initial";
  const winner = snapshot.phase === "ended" && snapshot.winnerIds?.includes(player.id);
  return (
    <section ref={area} style={style} className={`player-area ${mine ? "mine" : ""} ${active ? "active" : ""} ${winner ? "winner" : ""} ${snapshot.phase === "ended" ? "cards-revealed" : ""} ${snapshot.phase === "await_choice" && mine ? "replace-mode" : ""}`}>
      <div className="player-heading">
        <span className={`avatar avatar-${hash(player.id) % 5}`}>{player.name.slice(0, 1).toUpperCase()}</span>
        <div><b>{mine ? "You" : player.name}</b>{snapshot.phase === "ended" ? <small>{player.score} pts</small> : !player.connected && <small>Disconnected</small>}</div>
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
      <div className="card-grid">
        {player.cards.map((slot) => {
          const target = { playerId: player.id, slot: slot.slot };
          const isSelected = selected.some((item) => sameRef(item, target));
          const revealed = revealAt(snapshot, target);
          const power = canInteract ? powerHint(snapshot, target, slot.occupied) : undefined;
          const peekedByOther = snapshot.publicPeek?.viewerId !== snapshot.you.id && snapshot.publicPeek && sameRef(snapshot.publicPeek.target, target);
          return (
            <div className={`slot-wrap ${isSelected ? "selected" : ""} ${revealed ? "is-revealed" : ""} ${peekedByOther ? "peek-observed" : ""} ${power ? `power-target power-${power}` : ""}`} key={slot.slot} data-card-ref={refKey(target)}>
              <button
                className="card-button"
                disabled={!slot.occupied || !canInteract}
                draggable={slot.occupied && canInteract}
                aria-label={`${mine ? "Your" : player.name}'s card ${slot.slot + 1}${power ? ` · ${power === "peek" ? "peek target" : "swap target"}` : ""}`}
                onClick={(event) => { if (event.detail === 0) giftMode ? onGift(slot.slot) : onCard(target); }}
                onPointerUp={() => handleTap(target)}
                onDragStart={(event) => {
                  dragging.current = true;
                  event.dataTransfer.setData("application/x-cambio-card", JSON.stringify(target));
                }}
                onDragEnd={() => window.setTimeout(() => { dragging.current = false; }, 0)}
              >
                {slot.occupied ? (slot.card || revealed ? <PlayingCard card={slot.card ?? revealed!} compact flipped /> : <CardBack compact />) : <div className="empty-slot" />}
              </button>
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
  if (!isMyTurn) return null;
  let prompt = "";
  if (isMyTurn && snapshot.phase === "await_self_peek") prompt = "Peek at one of your cards";
  if (isMyTurn && snapshot.phase === "await_opponent_peek") prompt = "Peek at one opponent card";
  if (isMyTurn && snapshot.phase === "await_king_peek") prompt = "King: peek at an opponent card";
  if (isMyTurn && snapshot.phase === "await_swap") prompt = "Choose any two occupied cards to swap";
  if (snapshot.phase === "await_gift" && snapshot.pendingGift?.slapperId === snapshot.you.id) prompt = "Choose one card to give";
  if (!prompt) return null;
  return <div className="turn-prompt"><span className={isMyTurn ? "pulse" : ""} />{prompt}</div>;
}

function PlayingCard({ card, compact = false, flipped = false }: { card: Card; compact?: boolean; flipped?: boolean }) {
  const Face = faceFor(card);
  return (
    <div className={`playing-card asset-card ${compact ? "compact" : ""} ${isRed(card) ? "red" : "black"} ${flipped ? "flip-in" : ""}`} aria-label={cardLabel(card)}>
      <Suspense fallback={<span className="card-face-loading" />}>
        <Face title={cardLabel(card)} width="100%" height="100%" />
      </Suspense>
      {card.rank === 13 && isRed(card) && <em className="score-badge">−1</em>}
    </div>
  );
}

function CardBack({ compact = false }: { compact?: boolean }) {
  return <div className={`card-back ${compact ? "compact" : ""}`} aria-label="Face-down card" />;
}

function sameRef(a: CardRef, b: CardRef) {
  return a.playerId === b.playerId && a.slot === b.slot;
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

function uSeatStyle(index: number, total: number): CSSProperties {
  const spread = total <= 1 ? 0 : total === 2 ? 36 : total === 3 ? 64 : 84;
  const x = total <= 1 ? 50 : 50 - spread / 2 + (spread * index) / (total - 1);
  const edge = Math.abs(x - 50) / Math.max(spread / 2, 1);
  const y = 6 + 39 * edge ** 1.55;
  const scale = total <= 2 ? 1 : total <= 4 ? .8 : total <= 6 ? .66 : .56;
  return { "--u-x": `${x}%`, "--u-y": `${y}%`, "--u-scale": scale } as CSSProperties;
}

function animateAction(action: ActionView) {
  if (action.kind === "swap" && action.first && action.second) animateSwap(action.first, action.second);
  if (action.kind === "replace" && action.target) animateReplace(action.target);
  if (action.kind === "discard") animateDiscard();
  if (action.kind === "slap" && action.target) animateSlap(action.target);
  if (action.kind === "gift" && action.first && action.second) animateGift(action.first, action.second);
}

function slotFor(target: CardRef): HTMLElement | undefined {
  return [...document.querySelectorAll<HTMLElement>("[data-card-ref]")].find((slot) => slot.dataset.cardRef === refKey(target));
}

function cardFor(target: CardRef): HTMLElement | undefined {
  return slotFor(target)?.querySelector<HTMLElement>(".playing-card, .card-back") ?? undefined;
}

function animateSwap(first: CardRef, second: CardRef) {
  const a = cardFor(first);
  const b = cardFor(second);
  if (!a || !b) return;
  const aRect = a.getBoundingClientRect();
  const bRect = b.getBoundingClientRect();
  const landA = concealUntilLanding(a);
  const landB = concealUntilLanding(b);
  flyCard(a, bRect, aRect, -28, 0, 820, landA);
  flyCard(b, aRect, bRect, 28, 0, 820, landB);
}

function animateReplace(target: CardRef) {
  const targetCard = cardFor(target);
  const discardCard = document.querySelector<HTMLElement>(".discard-wrap .playing-card");
  const drawnPosition = document.querySelector<HTMLElement>(".drawn-card-position");
  if (!targetCard || !discardCard || !drawnPosition) return;
  const targetRect = targetCard.getBoundingClientRect();
  const discardRect = discardCard.getBoundingClientRect();
  const drawnRect = drawnPosition.getBoundingClientRect();
  const landDiscard = concealUntilLanding(discardCard);
  const landTarget = concealUntilLanding(targetCard);
  flyCard(discardCard, targetRect, discardRect, -30, 0, 680, landDiscard);
  flyCard(targetCard, drawnRect, targetRect, 34, 95, 760, landTarget);
}

function animateDiscard() {
  const drawnPosition = document.querySelector<HTMLElement>(".drawn-card-position");
  const discardCard = document.querySelector<HTMLElement>(".discard-wrap .playing-card");
  if (!drawnPosition || !discardCard) return;
  const land = concealUntilLanding(discardCard);
  flyCard(discardCard, drawnPosition.getBoundingClientRect(), discardCard.getBoundingClientRect(), -22, 0, 620, land);
}

function animateSlap(target: CardRef) {
  const source = slotFor(target);
  const discardCard = document.querySelector<HTMLElement>(".discard-wrap .playing-card");
  if (!source || !discardCard) return;
  const land = concealUntilLanding(discardCard);
  flyCard(discardCard, source.getBoundingClientRect(), discardCard.getBoundingClientRect(), -38, 0, 680, land);
}

function animateGift(source: CardRef, target: CardRef) {
  const sourceSlot = slotFor(source);
  const targetCard = cardFor(target);
  if (!sourceSlot || !targetCard) return;
  const land = concealUntilLanding(targetCard);
  flyCard(targetCard, sourceSlot.getBoundingClientRect(), targetCard.getBoundingClientRect(), 28, 0, 720, land);
}

function concealUntilLanding(element: HTMLElement) {
  const opacity = element.style.opacity;
  element.style.opacity = "0";
  return () => {
    element.style.opacity = opacity;
  };
}

function flyCard(card: HTMLElement, from: DOMRect, to: DOMRect, arc: number, delay: number, duration: number, land?: () => void) {
  const ghost = card.cloneNode(true) as HTMLElement;
  ghost.classList.add("action-card-ghost");
  Object.assign(ghost.style, {
    position: "fixed",
    left: `${from.left}px`,
    top: `${from.top}px`,
    width: `${from.width}px`,
    height: `${from.height}px`,
    margin: "0",
    zIndex: "100",
    pointerEvents: "none",
  });
  document.body.appendChild(ghost);
  const dx = to.left - from.left;
  const dy = to.top - from.top;
  const animation = ghost.animate([
    { transform: "translate3d(0,0,0) rotate(0deg) scale(1)", opacity: 1 },
    { transform: `translate3d(${dx * .5}px, ${dy * .5 + arc}px, 0) rotate(${arc > 0 ? 7 : -7}deg) scale(1.1)`, opacity: 1, offset: .52 },
    { transform: `translate3d(${dx}px, ${dy}px, 0) rotate(0deg) scale(1)`, opacity: 1 },
  ], { duration, delay, easing: "cubic-bezier(.2,.78,.2,1)", fill: "both" });
  animation.addEventListener("finish", () => {
    land?.();
    ghost.remove();
  }, { once: true });
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
