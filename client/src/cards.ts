import type { Card } from "../../shared/protocol";

const suitSymbols = {
  clubs: "♣",
  diamonds: "♦",
  hearts: "♥",
  spades: "♠",
  joker: "✦",
} as const;

export function rankLabel(rank: number): string {
  if (rank === 0) return "JOKER";
  if (rank === 1) return "A";
  if (rank === 11) return "J";
  if (rank === 12) return "Q";
  if (rank === 13) return "K";
  return String(rank);
}

export function cardLabel(card: Card): string {
  if (card.rank === 0) return "Joker";
  return `${rankLabel(card.rank)}${suitSymbols[card.suit]}`;
}

export function suitSymbol(card: Card): string {
  return suitSymbols[card.suit];
}

export function isRed(card: Card): boolean {
  return card.suit === "hearts" || card.suit === "diamonds";
}

