import { describe, expect, it } from "vitest";
import { cardLabel, rankLabel } from "./cards";

describe("card labels", () => {
  it("formats faces and red kings", () => {
    expect(rankLabel(12)).toBe("Q");
    expect(cardLabel({ id: "kh", rank: 13, suit: "hearts" })).toBe("K♥");
    expect(cardLabel({ id: "j", rank: 0, suit: "joker" })).toBe("Joker");
  });
});

