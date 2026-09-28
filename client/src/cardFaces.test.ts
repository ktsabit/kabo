import { describe, expect, it } from "vitest";
import type { Card, Suit } from "../../shared/protocol";
import { faceName } from "./cardFaces";

describe("playing-card face mapping", () => {
  it("maps every standard card and all four jokers to an asset", () => {
    const suits: Suit[] = ["clubs", "diamonds", "hearts", "spades"];
    for (const suit of suits) {
      for (let rank = 1; rank <= 13; rank += 1) {
        const card: Card = { id: `${suit}-${rank}`, suit, rank };
        expect(faceName(card)).toMatch(/^[CDHS](?:a|[2-9]|10|j|q|k)$/);
      }
    }
    for (let number = 1; number <= 4; number += 1) {
      expect(faceName({ id: `joker-${number}`, suit: "joker", rank: 0 })).toBe(number % 2 === 0 ? "J2" : "J1");
    }
  });
});
