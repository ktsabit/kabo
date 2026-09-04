import { describe, expect, it } from "vitest";
import { FLIGHT_SAMPLES, planFlightFrames } from "./flightMath";

describe("planFlightFrames", () => {
  it("lands a transform-only flight on the center-corrected destination", () => {
    const frames = planFlightFrames({
      from: { left: 20, top: 40, width: 50, height: 70 },
      to: { left: 200, top: 300, width: 70, height: 98 },
      arcSide: 1,
      arcRatio: .18,
      tilt: 7,
    });
    expect(frames.travel).toHaveLength(FLIGHT_SAMPLES + 1);
    expect(frames.travel[0].transform).toBe("translate3d(20px, 40px, 0)");
    expect(frames.travel.at(-1)?.transform).toBe("translate3d(210px, 314px, 0)");
    expect(frames.pose.at(-1)?.transform).toBe("rotate(0deg) scale3d(1.4,1.4,1)");
    for (const frame of [...frames.travel, ...frames.pose]) {
      expect(frame).not.toHaveProperty("left");
      expect(frame).not.toHaveProperty("top");
      expect(frame).not.toHaveProperty("width");
      expect(frame).not.toHaveProperty("height");
    }
  });

  it("uses an elevated curved midpoint rather than a linear diagonal", () => {
    const frames = planFlightFrames({
      from: { left: 0, top: 200, width: 60, height: 84 },
      to: { left: 300, top: 200, width: 60, height: 84 },
      arcSide: -1,
      arcRatio: .2,
      tilt: 6,
    });
    const midpoint = String(frames.travel[Math.floor(frames.travel.length / 2)].transform);
    const y = Number(midpoint.match(/, ([\d.-]+)px, 0\)/)?.[1]);
    expect(y).toBeLessThan(180);
  });
});
