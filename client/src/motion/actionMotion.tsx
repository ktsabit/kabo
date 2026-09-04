import { flushSync } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import type { ActionView } from "../../../shared/protocol";
import { CardBack, PlayingCard } from "../Card";
import { anchorKey, MOTION_TIMING, planActionMotion, type ActionMotionPlan, type CardFlightCue, type MotionAnchor } from "./actionCues";
import { planFlightFrames, type RectLike } from "./flightMath";

type CapturedAnchor = {
  element: HTMLElement;
  rect: DOMRect;
};

export type CapturedActionMotion = {
  plan: ActionMotionPlan;
  sources: Map<string, CapturedAnchor>;
  previousDiscard?: CapturedAnchor;
};

export type ActionMotionContext = {
  activePlayerId?: string;
  viewerId: string;
  actorName?: string;
};

type MeasuredAnchor = {
  element?: HTMLElement;
  rect: DOMRect;
};

type MountedFlight = {
  cue: CardFlightCue;
  outer: HTMLElement;
  pose: HTMLElement;
  roots: Root[];
};

type TableIndex = {
  cards: Map<string, HTMLElement>;
  zones: Map<string, HTMLElement>;
};

const CARD_SELECTOR = ".playing-card, .card-back, .empty-slot";
const DISCARD_SELECTOR = ".playing-card, .card-back, .empty-discard";

function visualInside(container: HTMLElement, selector: string): HTMLElement {
  return container.matches(selector) ? container : container.querySelector<HTMLElement>(selector) ?? container;
}

function indexTable(root: HTMLElement): TableIndex {
  const cards = new Map<string, HTMLElement>();
  const zones = new Map<string, HTMLElement>();
  for (const node of root.querySelectorAll<HTMLElement>("[data-card-ref], [data-motion-zone]")) {
    const cardRef = node.dataset.cardRef;
    if (cardRef !== undefined && !cards.has(cardRef)) cards.set(cardRef, visualInside(node, CARD_SELECTOR));
    const zone = node.dataset.motionZone;
    if (zone !== undefined && !zones.has(zone)) {
      zones.set(zone, visualInside(node, zone === "discard" ? DISCARD_SELECTOR : CARD_SELECTOR));
    }
  }
  return { cards, zones };
}

function elementFor(anchor: MotionAnchor, index: TableIndex): HTMLElement | undefined {
  if (anchor.kind === "card") return index.cards.get(`${anchor.ref.playerId}:${anchor.ref.slot}`);
  if (anchor.zone === "discard-side") return undefined;
  return index.zones.get(anchor.zone);
}

function cloneAnchor(element?: HTMLElement): CapturedAnchor | undefined {
  if (!element) return undefined;
  const clone = element.cloneNode(true) as HTMLElement;
  clone.classList.remove("flip-in", "penalty-arriving");
  clone.querySelectorAll(".flip-in, .penalty-arriving").forEach((node) => node.classList.remove("flip-in", "penalty-arriving"));
  return { element: clone, rect: element.getBoundingClientRect() };
}

function discardSideRect(discard: DOMRect): DOMRect {
  const viewport = window.visualViewport;
  const viewportLeft = viewport?.offsetLeft ?? 0;
  const viewportTop = viewport?.offsetTop ?? 0;
  const viewportRight = viewportLeft + (viewport?.width ?? document.documentElement.clientWidth);
  const viewportBottom = viewportTop + (viewport?.height ?? document.documentElement.clientHeight);
  const gap = 12;
  const right = discard.right + gap;
  const left = discard.left - discard.width - gap;
  const x = right + discard.width <= viewportRight - 8 ? right : Math.max(viewportLeft + 8, left);
  const y = Math.max(viewportTop + 8, Math.min(viewportBottom - discard.height - 8, discard.top + 8));
  return new DOMRect(x, y, discard.width, discard.height);
}

function waitForAnimations(animations: Animation[]): Promise<void> {
  return Promise.all(animations.map((animation) => animation.finished.catch(() => undefined))).then(() => undefined);
}

function flightColor(plan: ActionMotionPlan, cue: CardFlightCue, ownAction: boolean): string {
  switch (plan.actionKind) {
    case "swap": return cue.id === "swap-a" ? "#63eaf2" : "#c8a2ff";
    case "gift": return "#75e6b5";
    case "slap": return ownAction ? "#ffd36e" : "#a9b0ff";
    case "wrong_slap": return "#ff7267";
    case "late_slap": return "#a9b0ff";
    case "replace": return cue.id === "replace-out" ? "#ff9f78" : "#7ed8ed";
    case "discard": return "#ff9f78";
  }
}

function flightBadge(plan: ActionMotionPlan, cue: CardFlightCue): string | undefined {
  switch (plan.actionKind) {
    case "swap": return cue.id === "swap-a" ? "A" : "B";
    case "gift": return "GIVE";
    case "slap": return "SLAP";
    case "wrong_slap": return "MISS";
    case "late_slap": return "LATE";
    case "replace": return cue.id === "replace-out" ? "OUT" : "IN";
    case "discard": return "DROP";
  }
}

function actionLabel(plan: ActionMotionPlan, ownAction: boolean, actorName?: string): string {
  const actor = ownAction ? "YOU" : (actorName ?? "PLAYER").toLocaleUpperCase();
  switch (plan.actionKind) {
    case "swap": return `${actor} SWAP${ownAction ? "" : "S"}`;
    case "gift": return `${actor} GIVE${ownAction ? "" : "S"}`;
    case "slap": return `${actor} SLAP${ownAction ? "" : "S"}`;
    case "wrong_slap": return ownAction ? "YOUR SLAP MISSED" : `${actor} MISSED`;
    case "late_slap": return ownAction ? "YOUR SLAP WAS LATE" : `${actor} WAS LATE`;
    case "replace": return `${actor} REPLACE${ownAction ? "" : "S"}`;
    case "discard": return `${actor} DISCARD${ownAction ? "" : "S"}`;
  }
}

export class ActionMotionDirector {
  private readonly root: () => HTMLElement | null;
  private layer?: HTMLElement;
  private activeAnimations = new Set<Animation>();
  private cleanupCurrent?: () => void;
  private epoch = 0;

  constructor(root: () => HTMLElement | null) {
    this.root = root;
  }

  capture(action: ActionView): CapturedActionMotion {
    const plan = planActionMotion(action);
    const root = this.root();
    const sources = new Map<string, CapturedAnchor>();
    if (!root) return { plan, sources };
    const index = indexTable(root);
    for (const cue of plan.cues) {
      const key = anchorKey(cue.source);
      if (sources.has(key)) continue;
      const captured = cloneAnchor(elementFor(cue.source, index));
      if (captured) sources.set(key, captured);
    }
    const previousDiscard = plan.preserveDiscard
      ? cloneAnchor(index.zones.get("discard"))
      : undefined;
    return { plan, sources, previousDiscard };
  }

  async play(captured: CapturedActionMotion, context: ActionMotionContext): Promise<void> {
    this.cancel();
    const epoch = this.epoch;
    const root = this.root();
    if (!root || captured.plan.cues.length === 0 && !captured.plan.penaltyPlayerId) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

    // Read the post-action table once before applying any visibility or motion writes.
    const index = indexTable(root);
    const destinations = new Map<string, MeasuredAnchor>();
    const discard = index.zones.get("discard");
    const discardRect = discard?.getBoundingClientRect();
    for (const cue of captured.plan.cues) {
      const key = anchorKey(cue.destination);
      if (destinations.has(key)) continue;
      if (cue.destination.kind === "zone" && cue.destination.zone === "discard-side") {
        if (discardRect) destinations.set(key, { rect: discardSideRect(discardRect) });
        continue;
      }
      const element = elementFor(cue.destination, index);
      if (element) destinations.set(key, { element, rect: element.getBoundingClientRect() });
    }

    const hidden = [...new Set([...destinations.values()].flatMap((item) => item.element ? [item.element] : []))];
    const activeArea = context.activePlayerId
      ? [...root.querySelectorAll<HTMLElement>(".player-area")].find((area) => area.dataset.playerId === context.activePlayerId)
      : undefined;
    const ownAction = captured.plan.actorId === context.viewerId;
    const layer = this.motionLayer();
    layer.className = `action-motion-layer motion-${captured.plan.actionKind} ${ownAction ? "motion-own-action" : "motion-remote-action"}`;
    layer.replaceChildren();
    root.classList.add("action-in-flight");
    activeArea?.classList.add("action-active-held");
    hidden.forEach((element) => element.classList.add("action-visual-hidden"));

    const roots: Root[] = [];
    if (captured.previousDiscard && discardRect) {
      layer.appendChild(this.staticCard(captured.previousDiscard.element, discardRect));
    }

    const flights = captured.plan.cues.flatMap((cue) => {
      const source = captured.sources.get(anchorKey(cue.source));
      const destination = destinations.get(anchorKey(cue.destination));
      if (!source || !destination) return [];
      const color = flightColor(captured.plan, cue, ownAction);
      const badge = flightBadge(captured.plan, cue);
      const mounted = this.mountFlight(cue, source, destination.rect, color, badge);
      roots.push(...mounted.roots);
      const sourceMarker = this.marker(source.rect, color, "source", badge);
      const destinationMarker = this.marker(destination.rect, color, "destination", badge);
      layer.append(sourceMarker, destinationMarker);
      this.playMarker(sourceMarker, "source", cue.delayMs + cue.durationMs);
      this.playMarker(destinationMarker, "destination", cue.delayMs + cue.durationMs);
      layer.appendChild(mounted.outer);
      return [mounted];
    });
    const firstFlight = flights[0];
    if (firstFlight) {
      const sourceRects = flights.flatMap((flight) => {
        const source = captured.sources.get(anchorKey(flight.cue.source));
        return source ? [source.rect] : [];
      });
      if (sourceRects.length > 0) {
        const left = Math.min(...sourceRects.map((rect) => rect.left));
        const top = Math.min(...sourceRects.map((rect) => rect.top));
        const right = Math.max(...sourceRects.map((rect) => rect.right));
        const bottom = Math.max(...sourceRects.map((rect) => rect.bottom));
        const label = this.actionTag(
          actionLabel(captured.plan, ownAction, context.actorName),
          { left, top, width: right - left, height: bottom - top },
          flightColor(captured.plan, firstFlight.cue, ownAction),
        );
        layer.appendChild(label);
        this.animate(label, [
          { opacity: 0, transform: "translate3d(-50%, 6px, 0) scale(.96)" },
          { opacity: 1, transform: "translate3d(-50%, 0, 0) scale(1)", offset: .18 },
          { opacity: 1, transform: "translate3d(-50%, 0, 0) scale(1)", offset: .72 },
          { opacity: 0, transform: "translate3d(-50%, -4px, 0) scale(.98)" },
        ], { duration: firstFlight.cue.delayMs + firstFlight.cue.durationMs + 100, easing: "ease-out", fill: "forwards" });
      }
    }

    let cleaned = false;
    const cleanup = () => {
      if (cleaned) return;
      cleaned = true;
      hidden.forEach((element) => element.classList.remove("action-visual-hidden"));
      activeArea?.classList.remove("action-active-held");
      root.classList.remove("action-in-flight");
      root.querySelectorAll(".card-grid.hand-compacting").forEach((grid) => grid.classList.remove("hand-compacting"));
      root.querySelectorAll(".penalty-arriving").forEach((card) => card.classList.remove("penalty-arriving"));
      roots.forEach((mountedRoot) => mountedRoot.unmount());
      if (this.layer === layer) layer.replaceChildren();
      if (this.cleanupCurrent === cleanup) this.cleanupCurrent = undefined;
    };
    this.cleanupCurrent = cleanup;

    try {
      const travel = flights.flatMap((mounted) => this.playTravel(mounted, captured.sources.get(anchorKey(mounted.cue.source))!.rect, destinations.get(anchorKey(mounted.cue.destination))!.rect));
      await waitForAnimations(travel);
      if (epoch !== this.epoch) return;

      // Destination cards are already mounted. Reveal them under the landed
      // flyer, then fade only the overlay during a short handoff settle.
      hidden.forEach((element) => element.classList.remove("action-visual-hidden"));
      const settles = flights.map(({ outer, cue }) => this.animate(outer, [
        { opacity: 1 },
        { opacity: 0 },
      ], { duration: cue.handoff ? 72 : 120, easing: "cubic-bezier(.4,0,1,1)", fill: "forwards" }));
      await waitForAnimations(settles);
      if (epoch !== this.epoch) return;

      if (captured.plan.compactPlayerId) await this.compactHand(root, captured.plan.compactPlayerId);
      if (captured.plan.penaltyPlayerId) await this.playPenalty(root, captured.plan.penaltyPlayerId);
    } finally {
      if (epoch === this.epoch) cleanup();
    }
  }

  cancel(): void {
    this.epoch += 1;
    for (const animation of this.activeAnimations) animation.cancel();
    this.activeAnimations.clear();
    this.cleanupCurrent?.();
    this.cleanupCurrent = undefined;
    this.layer?.replaceChildren();
  }

  destroy(): void {
    this.cancel();
    this.layer?.remove();
    this.layer = undefined;
  }

  private motionLayer(): HTMLElement {
    if (this.layer?.isConnected) return this.layer;
    const layer = document.createElement("div");
    layer.className = "action-motion-layer";
    layer.setAttribute("aria-hidden", "true");
    document.body.appendChild(layer);
    this.layer = layer;
    return layer;
  }

  private staticCard(source: HTMLElement, destination: RectLike): HTMLElement {
    const plate = document.createElement("div");
    plate.className = "action-card-static";
    plate.appendChild(source.cloneNode(true));
    Object.assign(plate.style, {
      left: `${destination.left}px`,
      top: `${destination.top}px`,
      width: `${destination.width}px`,
      height: `${destination.height}px`,
    });
    return plate;
  }

  private marker(rect: RectLike, color: string, role: "source" | "destination", badge?: string): HTMLElement {
    const marker = document.createElement("div");
    marker.className = `action-card-marker action-card-marker-${role}`;
    marker.style.setProperty("--flight-color", color);
    Object.assign(marker.style, {
      left: `${rect.left}px`,
      top: `${rect.top}px`,
      width: `${rect.width}px`,
      height: `${rect.height}px`,
    });
    if (badge) {
      const chip = document.createElement("span");
      chip.textContent = badge;
      marker.appendChild(chip);
    }
    return marker;
  }

  private playMarker(marker: HTMLElement, role: "source" | "destination", duration: number): void {
    if (role === "source") {
      this.animate(marker, [
        { opacity: .88, transform: "scale(1)" },
        { opacity: .42, transform: "scale(1.12)", offset: .52 },
        { opacity: 0, transform: "scale(1.18)" },
      ], { duration, easing: "ease-out", fill: "forwards" });
      return;
    }
    this.animate(marker, [
      { opacity: .12, transform: "scale(.9)" },
      { opacity: .2, transform: "scale(.94)", offset: .45 },
      { opacity: .95, transform: "scale(1.08)", offset: .84 },
      { opacity: .55, transform: "scale(1)" },
    ], { duration, easing: "cubic-bezier(.2,.8,.2,1)", fill: "forwards" });
  }

  private actionTag(label: string, sourceGroup: RectLike, color: string): HTMLElement {
    const tag = document.createElement("div");
    tag.className = "action-motion-label";
    tag.textContent = label;
    tag.style.setProperty("--flight-color", color);
    const viewport = window.visualViewport;
    const viewportLeft = viewport?.offsetLeft ?? 0;
    const viewportWidth = viewport?.width ?? document.documentElement.clientWidth;
    const viewportRight = viewportLeft + viewportWidth;
    const groupRight = sourceGroup.left + sourceGroup.width;
    const roomOnRight = viewportRight - groupRight;
    const roomOnLeft = sourceGroup.left - viewportLeft;
    let center = sourceGroup.left + sourceGroup.width / 2;
    let top = Math.max((viewport?.offsetTop ?? 0) + 7, sourceGroup.top - 31);
    if (roomOnRight >= 118) {
      center = groupRight + 59;
      top = sourceGroup.top + sourceGroup.height / 2 - 9;
    } else if (roomOnLeft >= 118) {
      center = sourceGroup.left - 59;
      top = sourceGroup.top + sourceGroup.height / 2 - 9;
    }
    Object.assign(tag.style, {
      left: `${Math.max(viewportLeft + 54, Math.min(viewportRight - 54, center))}px`,
      top: `${top}px`,
    });
    return tag;
  }

  private mountFlight(cue: CardFlightCue, source: CapturedAnchor, destination: RectLike, color: string, badge?: string): MountedFlight {
    const outer = document.createElement("div");
    outer.className = `action-card-ghost ${cue.className ?? ""}`.trim();
    outer.style.setProperty("--flight-color", color);
    Object.assign(outer.style, {
      width: `${source.rect.width}px`,
      height: `${source.rect.height}px`,
    });
    const trail = document.createElement("div");
    trail.className = "action-card-trail";
    const pose = document.createElement("div");
    pose.className = "action-card-pose";
    const visual = document.createElement("div");
    visual.className = "action-card-visual";
    const roots: Root[] = [];

    const shouldFlip = cue.flipToBack && !source.element.matches(".card-back");
    if (shouldFlip) {
      const flipper = document.createElement("div");
      flipper.className = "action-card-flipper";
      const front = document.createElement("div");
      front.className = "action-card-layer action-card-front";
      front.appendChild(source.element.cloneNode(true));
      const back = document.createElement("div");
      back.className = "action-card-layer action-card-back";
      const backRoot = createRoot(back);
      flushSync(() => backRoot.render(<CardBack compact />));
      roots.push(backRoot);
      flipper.append(front, back);
      visual.appendChild(flipper);
    } else if (cue.visual.kind === "face") {
      const face = cue.visual.card;
      const cardRoot = createRoot(visual);
      flushSync(() => cardRoot.render(<PlayingCard card={face} compact />));
      roots.push(cardRoot);
    } else if (cue.visual.kind === "back") {
      const backRoot = createRoot(visual);
      flushSync(() => backRoot.render(<CardBack compact />));
      roots.push(backRoot);
    } else {
      visual.appendChild(source.element.cloneNode(true));
    }
    pose.appendChild(visual);
    if (badge) {
      const chip = document.createElement("span");
      chip.className = "action-card-flight-badge";
      chip.textContent = badge;
      pose.appendChild(chip);
    }
    outer.append(trail, pose);
    return { cue, outer, pose, roots };
  }

  private playTravel(mounted: MountedFlight, from: DOMRect, to: RectLike): Animation[] {
    const { travel, pose } = planFlightFrames({
      from,
      to,
      arcSide: mounted.cue.arcSide,
      arcRatio: mounted.cue.arcRatio,
      tilt: mounted.cue.tilt,
    });
    const options: KeyframeAnimationOptions = {
      duration: mounted.cue.durationMs,
      delay: mounted.cue.delayMs,
      easing: "linear",
      fill: "forwards",
    };
    const animations = [
      this.animate(mounted.outer, travel, options),
      this.animate(mounted.pose, pose, options),
    ];
    const flipper = mounted.outer.querySelector<HTMLElement>(".action-card-flipper");
    if (flipper) {
      animations.push(this.animate(flipper, [
        { transform: "rotateY(0deg)", offset: 0 },
        { transform: "rotateY(0deg)", offset: .58 },
        { transform: "rotateY(180deg)", offset: 1 },
      ], { ...options, easing: "cubic-bezier(.45,.05,.55,.95)" }));
    }
    return animations;
  }

  private async compactHand(root: HTMLElement, playerId: string): Promise<void> {
    const grid = [...root.querySelectorAll<HTMLElement>(".player-area")]
      .find((area) => area.dataset.playerId === playerId)
      ?.querySelector<HTMLElement>(".card-grid");
    if (!grid) return;
    const cards = [...grid.querySelectorAll<HTMLElement>(".slot-wrap:not(.empty-slot-anchor)")];
    const before = new Map(cards.map((card) => [card.dataset.cardRef, card.getBoundingClientRect()]));
    grid.classList.add("hand-compacting");
    await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));
    const animations = cards.flatMap((card) => {
      const previous = before.get(card.dataset.cardRef);
      if (!previous) return [];
      const next = card.getBoundingClientRect();
      const x = previous.left - next.left;
      const y = previous.top - next.top;
      if (Math.hypot(x, y) < .75) return [];
      return [this.animate(card, [
        { transform: `translate3d(${x}px, ${y}px, 0)` },
        { transform: "translate3d(0, 0, 0)" },
      ], { duration: MOTION_TIMING.compactMs, easing: "cubic-bezier(.2,.8,.2,1)", fill: "both" })];
    });
    await waitForAnimations(animations);
  }

  private async playPenalty(root: HTMLElement, playerId: string): Promise<void> {
    const area = [...root.querySelectorAll<HTMLElement>(".player-area")].find((item) => item.dataset.playerId === playerId);
    if (!area) return;
    area.querySelector<HTMLElement>(".penalty-card-pending")?.classList.add("penalty-arriving");
    const rect = area.getBoundingClientRect();
    const flash = document.createElement("div");
    flash.className = "slap-flash";
    flash.style.setProperty("--flash-x", `${rect.left + rect.width / 2}px`);
    flash.style.setProperty("--flash-y", `${rect.top + rect.height / 2}px`);
    this.motionLayer().appendChild(flash);
    const animations = [
      this.animate(flash, [{ opacity: 0 }, { opacity: 1, offset: .25 }, { opacity: 0 }], { duration: 360, easing: "ease-out" }),
      this.animate(area, [
        { translate: "0 0" },
        { translate: "-8px 0" },
        { translate: "7px 0" },
        { translate: "-4px 0" },
        { translate: "0 0" },
      ], { duration: 340, easing: "ease-out" }),
    ];
    await waitForAnimations(animations);
    flash.remove();
  }

  private animate(element: Element, keyframes: Keyframe[] | PropertyIndexedKeyframes, options?: number | KeyframeAnimationOptions): Animation {
    const animation = element.animate(keyframes, options);
    this.activeAnimations.add(animation);
    const forget = () => this.activeAnimations.delete(animation);
    animation.addEventListener("finish", forget, { once: true });
    animation.addEventListener("cancel", forget, { once: true });
    return animation;
  }
}
