import { describe, expect, it } from "vitest";
import { ActionSequence } from "./actionSequence";

type TestSnapshot = { serial: number; action?: { id: number } };

describe("ActionSequence", () => {
  it("plays each rapid action once and keeps the latest deferred snapshot", () => {
    const sequence = new ActionSequence<{ id: number }, TestSnapshot>();
    expect(sequence.ingest({ serial: 0 }, false)).toBe("render");

    for (let id = 1; id <= 80; id += 1) {
      expect(sequence.ingest({ serial: id * 10, action: { id } }, true)).toBe("queue");
      expect(sequence.ingest({ serial: id * 10 + 1, action: { id } }, true)).toBe("defer");
      if (id % 3 === 0) sequence.ingest({ serial: id * 10 + 2 }, true);
    }

    const played: number[] = [];
    let next = sequence.beginNext();
    while (next) {
      played.push(next.action.id);
      sequence.finish(next.action.id);
      next = sequence.beginNext();
    }

    expect(played).toEqual(Array.from({ length: 80 }, (_, index) => index + 1));
    expect(sequence.debugState()).toMatchObject({ queuedIds: [], runningActionId: undefined });
  });

  it("drops interrupted work and adopts a reconnect snapshot as the new cursor", () => {
    const sequence = new ActionSequence<{ id: number }, TestSnapshot>();
    sequence.ingest({ serial: 0 }, false);
    sequence.ingest({ serial: 1, action: { id: 41 } }, true);
    expect(sequence.beginNext()?.action.id).toBe(41);
    sequence.ingest({ serial: 2, action: { id: 42 } }, true);

    sequence.recover({ serial: 99, action: { id: 75 } });
    expect(sequence.debugState()).toEqual({
      lastActionId: 75,
      queuedIds: [],
      runningActionId: undefined,
      hasDeferred: false,
    });
    expect(sequence.ingest({ serial: 100, action: { id: 76 } }, true)).toBe("queue");
    expect(sequence.beginNext()?.action.id).toBe(76);
  });

  it("detects a server cursor reset instead of suppressing all future animations", () => {
    const sequence = new ActionSequence<{ id: number }, TestSnapshot>();
    sequence.ingest({ serial: 1, action: { id: 90 } }, false);
    expect(sequence.ingest({ serial: 2, action: { id: 1 } }, true)).toBe("restart");
  });

  it("fuzzes bursty duplicate, actionless, and interrupted snapshot delivery", () => {
    for (let seed = 1; seed <= 64; seed += 1) {
      let random = seed;
      const nextRandom = () => {
        random = (random * 48271) % 0x7fffffff;
        return random;
      };
      const sequence = new ActionSequence<{ id: number }, TestSnapshot>();
      sequence.ingest({ serial: 0 }, false);
      const played: number[] = [];
      let nextId = 1;

      for (let step = 0; step < 240; step += 1) {
        const choice = nextRandom() % 7;
        if (choice <= 2) {
          sequence.ingest({ serial: step, action: { id: nextId } }, true);
          if (choice === 0) sequence.ingest({ serial: step + 1_000, action: { id: nextId } }, true);
          nextId += 1;
        } else if (choice === 3) {
          sequence.ingest({ serial: step }, true);
        } else {
          const running = sequence.beginNext();
          if (running) {
            played.push(running.action.id);
            sequence.finish(running.action.id);
          }
        }
      }

      let pending = sequence.beginNext();
      while (pending) {
        played.push(pending.action.id);
        sequence.finish(pending.action.id);
        pending = sequence.beginNext();
      }
      expect(new Set(played).size, `duplicate playback for seed ${seed}`).toBe(played.length);
      expect(played, `out-of-order playback for seed ${seed}`).toEqual([...played].sort((a, b) => a - b));
      expect(sequence.debugState().queuedIds).toEqual([]);
    }
  });
});
