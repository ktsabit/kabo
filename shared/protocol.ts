export type Suit = "clubs" | "diamonds" | "hearts" | "spades" | "joker";

export interface Card {
  id: string;
  rank: number;
  suit: Suit;
}

export interface CardRef {
  playerId: string;
  slot: number;
}

export type GamePhase =
  | "lobby"
  | "initial_peek"
  | "await_draw"
  | "await_choice"
  | "await_self_peek"
  | "await_opponent_peek"
  | "reveal_self"
  | "reveal_opponent"
  | "await_swap"
  | "await_king_peek"
  | "reveal_king"
  | "await_gift"
  | "ended";

export interface CardSlotView {
  slot: number;
  occupied: boolean;
  card?: Card;
}

export interface PlayerView {
  id: string;
  name: string;
  connected: boolean;
  cards: CardSlotView[];
  initialReady?: boolean;
  score?: number;
}

export interface RosterPlayerView {
  id: string;
  name: string;
  connected: boolean;
  joiningNextRound: boolean;
  ready: boolean;
}

export interface RevealView {
  kind: "initial" | "self" | "opponent" | "king";
  cards: Array<{ target: CardRef; card: Card }>;
}

export interface GiftView {
  slapperId: string;
  target: CardRef;
}

export interface PublicPeekView {
  viewerId: string;
  target: CardRef;
}

export interface ActionView {
  id: number;
  kind: "swap" | "replace" | "discard" | "slap" | "gift";
  actorId: string;
  first?: CardRef;
  second?: CardRef;
  target?: CardRef;
}

export interface SnapshotMessage {
  type: "snapshot";
  roomId: string;
  you: { id: string; name: string };
  youRole: "active" | "spectator";
  nextRoundJoined: boolean;
  nextRoundFull: boolean;
  youReady: boolean;
  allReady: boolean;
  nextStarterId?: string;
  nextRoundPlayers: RosterPlayerView[];
  waitingPlayers: RosterPlayerView[];
  phase: GamePhase;
  currentPlayerId?: string;
  players: PlayerView[];
  drawPileCount: number;
  hasDrawnCard: boolean;
  discardTop?: Card;
  discardEventId?: number;
  drawnCard?: Card;
  reveal?: RevealView;
  publicPeek?: PublicPeekView;
  action?: ActionView;
  pendingGift?: GiftView;
  endReason?: string;
  winnerIds?: string[];
}

export interface ErrorMessage {
  type: "error";
  code: string;
  message: string;
}

export interface NoticeMessage {
  type: "notice";
  message: string;
}

export type ServerMessage = SnapshotMessage | ErrorMessage | NoticeMessage;

export type ClientMessage =
  | { type: "start_game" }
  | { type: "set_next_round"; joinNextRound: boolean }
  | { type: "set_ready"; ready: boolean }
  | { type: "acknowledge_initial" }
  | { type: "draw" }
  | { type: "replace"; slot: number }
  | { type: "discard_drawn" }
  | { type: "peek"; target: CardRef }
  | { type: "acknowledge_reveal" }
  | { type: "swap"; first: CardRef; second: CardRef }
  | { type: "slap"; eventId: number; target: CardRef }
  | { type: "gift"; sourceSlot: number }
  | { type: "call_end" };

export function cardScore(card: Pick<Card, "rank" | "suit">): number {
  if (card.rank === 0) return 0;
  if (card.rank === 13 && (card.suit === "hearts" || card.suit === "diamonds")) return -1;
  return card.rank;
}
