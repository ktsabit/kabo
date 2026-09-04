export type RectLike = Pick<DOMRect, "left" | "top" | "width" | "height">;

export type FlightPath = {
  from: RectLike;
  to: RectLike;
  arcSide: -1 | 1;
  arcRatio: number;
  tilt: number;
};

export type FlightFrames = {
  travel: Keyframe[];
  pose: Keyframe[];
};

export const FLIGHT_SAMPLES = 16;

function easeInOut(t: number): number {
  return t < .5 ? 2 * t * t : 1 - ((-2 * t + 2) ** 2) / 2;
}

function quadratic(start: number, control: number, end: number, t: number): number {
  const inverse = 1 - t;
  return inverse * inverse * start + 2 * inverse * t * control + t * t * end;
}

export function planFlightFrames(path: FlightPath): FlightFrames {
  const fromX = path.from.left;
  const fromY = path.from.top;
  const toX = path.to.left + (path.to.width - path.from.width) / 2;
  const toY = path.to.top + (path.to.height - path.from.height) / 2;
  const distance = Math.hypot(toX - fromX, toY - fromY);
  const arc = Math.min(112, Math.max(28, distance * path.arcRatio));
  const controlX = (fromX + toX) / 2 + path.arcSide * Math.min(26, distance * .05);
  const controlY = Math.min(fromY, toY) - arc;
  const travel: Keyframe[] = [];
  for (let index = 0; index <= FLIGHT_SAMPLES; index += 1) {
    const offset = index / FLIGHT_SAMPLES;
    const t = easeInOut(offset);
    const x = quadratic(fromX, controlX, toX, t);
    const y = quadratic(fromY, controlY, toY, t);
    travel.push({ transform: `translate3d(${x}px, ${y}px, 0)`, offset });
  }
  const scaleX = path.from.width > 0 ? path.to.width / path.from.width : 1;
  const scaleY = path.from.height > 0 ? path.to.height / path.from.height : 1;
  const pose: Keyframe[] = [
    { transform: "rotate(0deg) scale3d(.98,.98,1)", offset: 0 },
    { transform: `rotate(${path.arcSide * path.tilt}deg) scale3d(1.035,1.035,1)`, offset: .46 },
    { transform: `rotate(0deg) scale3d(${scaleX},${scaleY},1)`, offset: 1 },
  ];
  return { travel, pose };
}
