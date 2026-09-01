export type SequencedAction = { id: number };

export type SequenceDecision = "render" | "queue" | "defer" | "ignore" | "restart";

export class ActionSequence<A extends SequencedAction, S extends { action?: A }> {
  private lastActionId = 0;
  private queued: Array<{ action: A; snapshot: S }> = [];
  private runningActionId: number | undefined;
  private deferredSnapshot: S | undefined;

  ingest(snapshot: S, hasBaseline: boolean): SequenceDecision {
    const action = snapshot.action;
    if (!hasBaseline) {
      this.recover(snapshot);
      return "render";
    }
    if (!action) {
      if (this.runningActionId !== undefined || this.queued.length > 0) {
        this.deferredSnapshot = snapshot;
        return "defer";
      }
      return "render";
    }
    if (action.id < this.lastActionId) return "restart";
    if (action.id === this.lastActionId) {
      const waiting = this.queued.find((item) => item.action.id === action.id);
      if (waiting) {
        waiting.snapshot = snapshot;
        return "defer";
      }
      if (this.runningActionId === action.id) {
        this.deferredSnapshot = snapshot;
        return "defer";
      }
      if (this.runningActionId === undefined && this.queued.length === 0) return "render";
      return "ignore";
    }

    this.lastActionId = action.id;
    this.queued.push({ action, snapshot });
    return "queue";
  }

  beginNext(): { action: A; snapshot: S } | undefined {
    if (this.runningActionId !== undefined) return undefined;
    const next = this.queued.shift();
    if (next) this.runningActionId = next.action.id;
    return next;
  }

  finish(actionId: number): S | undefined {
    if (this.runningActionId === actionId) this.runningActionId = undefined;
    if (this.queued.length > 0) return undefined;
    const deferred = this.deferredSnapshot;
    this.deferredSnapshot = undefined;
    return deferred;
  }

  recover(snapshot?: S) {
    this.queued = [];
    this.runningActionId = undefined;
    this.deferredSnapshot = undefined;
    this.lastActionId = snapshot?.action?.id ?? 0;
  }

  hasQueued() {
    return this.queued.length > 0;
  }

  isBusy() {
    return this.runningActionId !== undefined || this.queued.length > 0;
  }

  debugState() {
    return {
      lastActionId: this.lastActionId,
      queuedIds: this.queued.map((item) => item.action.id),
      runningActionId: this.runningActionId,
      hasDeferred: this.deferredSnapshot !== undefined,
    };
  }
}
