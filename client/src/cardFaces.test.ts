import { describe, expect, it } from "vitest";
import type { Card, Suit } from "../../shared/protocol";
import { faceName } from "./cardFaces";

describe("playing-card face mapping", () => {
  it("maps every standard card and both jokers to an asset", () => {
    const suits: Suit[] = ["clubs", "diamonds", "hearts", "spades"];
    for (const suit of suits) {
      for (let rank = 1; rank <= 13; rank += 1) {
        const card: Card = { id: `${suit}-${rank}`, suit, rank };
        expect(faceName(card)).toMatch(/^[CDHS](?:a|[2-9]|10|j|q|k)$/);
      }
    }
    expect(faceName({ id: "joker-1", suit: "joker", rank: 0 })).toBe("J1");
    expect(faceName({ id: "joker-2", suit: "joker", rank: 0 })).toBe("J2");
  });
});
