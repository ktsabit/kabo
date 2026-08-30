import { lazy, type ComponentType, type LazyExoticComponent, type SVGProps } from "react";
import type { Card } from "../../shared/protocol";

type FaceComponent = ComponentType<SVGProps<SVGSVGElement> & { title?: string }>;
const cache = new Map<string, LazyExoticComponent<FaceComponent>>();

const suitPrefix = {
  clubs: "C",
  diamonds: "D",
  hearts: "H",
  spades: "S",
} as const;

function rankSuffix(rank: number): string {
  if (rank === 1) return "a";
  if (rank === 11) return "j";
  if (rank === 12) return "q";
  if (rank === 13) return "k";
  return String(rank);
}

export function faceName(card: Card): string {
  if (card.rank === 0 || card.suit === "joker") {
    return card.id.endsWith("2") ? "J2" : "J1";
  }
  return `${suitPrefix[card.suit]}${rankSuffix(card.rank)}`;
}

export function faceFor(card: Card): LazyExoticComponent<FaceComponent> {
  const name = faceName(card);
  const existing = cache.get(name);
  if (existing) return existing;
  const component = lazy(async () => {
    // The CC0 package publishes components but points to a missing type folder.
    // @ts-expect-error Missing declarations in @letele/playing-cards 0.1.0.
    const module = await import("@letele/playing-cards/dist/index.esm.js") as Record<string, FaceComponent>;
    return { default: module[name] };
  });
  cache.set(name, component);
  return component;
}
