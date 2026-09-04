import { describe, expect, it } from "vitest";
import type { ActionView, Card, CardRef } from "../../../shared/protocol";
import { MOTION_TIMING, planActionMotion } from "./actionCues";

const first: CardRef = { playerId: "p1", slot: 0 };
const second: CardRef = { playerId: "p2", slot: 2 };
const face: Card = { id: "card-7h", rank: 7, suit: "hearts" };

function action(value: Partial<ActionView> & Pick<ActionView, "kind">): ActionView {
  return { id: 12, actorId: "p1", ...value };
}

describe("planActionMotion", () => {
  it("plans a reciprocal, synchronized swap", () => {
    const plan = planActionMotion(action({ kind: "swap", first, second }));
    expect(plan.cues).toHaveLength(2);
    expect(plan.cues.map((cue) => [cue.source, cue.destination])).toEqual([
      [{ kind: "card", ref: first }, { kind: "card", ref: second }],
      [{ kind: "card", ref: second }, { kind: "card", ref: first }],
    ]);
    expect(plan.cues.every((cue) => cue.durationMs === MOTION_TIMING.swapMs && cue.handoff)).toBe(true);
  });

  it("plans replace as two overlapping flights into mounted destinations", () => {
    const plan = planActionMotion(action({ kind: "replace", target: first, card: face }));
    expect(plan.preserveDiscard).toBe(true);
    expect(plan.cues.map((cue) => cue.id)).toEqual(["replace-out", "replace-in"]);
    expect(plan.cues[0].visual).toEqual({ kind: "face", card: face });
    expect(plan.cues[1]).toMatchObject({ delayMs: 45, flipToBack: true, handoff: true });
  });

  it("keeps rejected slaps outside the discard and attaches penalties only to wrong slaps", () => {
    const wrong = planActionMotion(action({ kind: "wrong_slap", target: second, card: face }));
    const late = planActionMotion(action({ kind: "late_slap", target: second, card: face }));
    expect(wrong.cues[0].destination).toEqual({ kind: "zone", zone: "discard-side" });
    expect(wrong.cues[0]).toMatchObject({ returnToSource: true, returnDurationMs: MOTION_TIMING.rejectedSlapReturnMs });
    expect(late.cues[0]).toMatchObject({ returnToSource: true, returnDurationMs: MOTION_TIMING.rejectedSlapReturnMs });
    expect(wrong.penaltyPlayerId).toBe("p1");
    expect(late.penaltyPlayerId).toBeUndefined();
  });
});
