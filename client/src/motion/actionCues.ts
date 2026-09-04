import type { ActionView, Card, CardRef } from "../../../shared/protocol";

export const MOTION_TIMING = {
  swapMs: 430,
  replaceOutMs: 460,
  replaceInMs: 410,
  discardMs: 360,
  slapMs: 390,
  giftMs: 400,
  rejectedSlapMs: 430,
  compactMs: 220,
} as const;

export type MotionAnchor =
  | { kind: "card"; ref: CardRef }
  | { kind: "zone"; zone: "drawn" | "discard" | "discard-side" };

export type FlightVisual =
  | { kind: "source" }
  | { kind: "back" }
  | { kind: "face"; card: Card };

export type CardFlightCue = {
  id: string;
  type: "flight";
  source: MotionAnchor;
  destination: MotionAnchor;
  visual: FlightVisual;
  durationMs: number;
  delayMs: number;
  arcSide: -1 | 1;
  arcRatio: number;
  tilt: number;
  flipToBack?: boolean;
  handoff: boolean;
  className?: string;
};

export type ActionMotionPlan = {
  actionId: number;
  actionKind: ActionView["kind"];
  actorId: string;
  cues: CardFlightCue[];
  preserveDiscard: boolean;
  compactPlayerId?: string;
  penaltyPlayerId?: string;
};

const card = (ref: CardRef): MotionAnchor => ({ kind: "card", ref });
const zone = (name: "drawn" | "discard" | "discard-side"): MotionAnchor => ({ kind: "zone", zone: name });

function flight(
  id: string,
  source: MotionAnchor,
  destination: MotionAnchor,
  visual: FlightVisual,
  options: Omit<CardFlightCue, "id" | "type" | "source" | "destination" | "visual">,
): CardFlightCue {
  return { id, type: "flight", source, destination, visual, ...options };
}

export function planActionMotion(action: ActionView): ActionMotionPlan {
  const base: ActionMotionPlan = {
    actionId: action.id,
    actionKind: action.kind,
    actorId: action.actorId,
    cues: [],
    preserveDiscard: false,
  };
  switch (action.kind) {
    case "swap":
      if (!action.first || !action.second) return base;
      return {
        ...base,
        cues: [
          flight("swap-a", card(action.first), card(action.second), { kind: "back" }, {
            durationMs: MOTION_TIMING.swapMs, delayMs: 0, arcSide: -1, arcRatio: .18, tilt: 5, handoff: true, className: "swap-card-flight",
          }),
          flight("swap-b", card(action.second), card(action.first), { kind: "back" }, {
            durationMs: MOTION_TIMING.swapMs, delayMs: 0, arcSide: 1, arcRatio: .18, tilt: 5, handoff: true, className: "swap-card-flight",
          }),
        ],
      };
    case "replace":
      if (!action.target || !action.card) return base;
      return {
        ...base,
        preserveDiscard: true,
        cues: [
          flight("replace-out", card(action.target), zone("discard"), { kind: "face", card: action.card }, {
            durationMs: MOTION_TIMING.replaceOutMs, delayMs: 0, arcSide: -1, arcRatio: .16, tilt: 8, handoff: true,
          }),
          flight("replace-in", zone("drawn"), card(action.target), { kind: "source" }, {
            durationMs: MOTION_TIMING.replaceInMs, delayMs: 45, arcSide: 1, arcRatio: .17, tilt: 7, flipToBack: true, handoff: true,
          }),
        ],
      };
    case "discard":
      if (!action.card) return base;
      return {
        ...base,
        preserveDiscard: true,
        cues: [flight("discard", zone("drawn"), zone("discard"), { kind: "face", card: action.card }, {
          durationMs: MOTION_TIMING.discardMs, delayMs: 0, arcSide: -1, arcRatio: .15, tilt: 7, handoff: true,
        })],
      };
    case "slap":
      if (!action.target || !action.card) return base;
      return {
        ...base,
        preserveDiscard: true,
        compactPlayerId: action.target.playerId,
        cues: [flight("slap", card(action.target), zone("discard"), { kind: "face", card: action.card }, {
          durationMs: MOTION_TIMING.slapMs, delayMs: 0, arcSide: -1, arcRatio: .18, tilt: 9, handoff: true,
        })],
      };
    case "gift":
      if (!action.first || !action.second) return base;
      return {
        ...base,
        cues: [flight("gift", card(action.first), card(action.second), { kind: "source" }, {
          durationMs: MOTION_TIMING.giftMs, delayMs: 0, arcSide: 1, arcRatio: .17, tilt: 7, handoff: true,
        })],
      };
    case "wrong_slap":
    case "late_slap":
      if (!action.target || !action.card) {
        return { ...base, penaltyPlayerId: action.kind === "wrong_slap" ? action.actorId : undefined };
      }
      return {
        ...base,
        penaltyPlayerId: action.kind === "wrong_slap" ? action.actorId : undefined,
        cues: [flight(action.kind, card(action.target), zone("discard-side"), { kind: "face", card: action.card }, {
          durationMs: MOTION_TIMING.rejectedSlapMs,
          delayMs: 0,
          arcSide: -1,
          arcRatio: .2,
          tilt: 9,
          handoff: false,
          className: action.kind === "wrong_slap" ? "wrong-slap-flight" : "late-slap-flight",
        })],
      };
  }
}

export function anchorKey(anchor: MotionAnchor): string {
  return anchor.kind === "card" ? `card:${anchor.ref.playerId}:${anchor.ref.slot}` : `zone:${anchor.zone}`;
}
