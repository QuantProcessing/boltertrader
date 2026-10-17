import { describe, expect, it } from "vitest";

import { distanceToSegment, duration, intervalSeconds, logicalToTime, rayEnd, timeToLogical } from "./chart-drawings";

const times = [1000, 1060, 1120, 1240];

describe("time <-> logical mapping", () => {
  it("maps bar times to their index and interpolates between bars", () => {
    expect(timeToLogical(times, 60, 1060)).toBe(1);
    expect(timeToLogical(times, 60, 1180)).toBe(2.5);
  });

  it("extrapolates by the interval outside the loaded bars", () => {
    expect(timeToLogical(times, 60, 880)).toBe(-2);
    expect(timeToLogical(times, 60, 1360)).toBe(5);
    expect(logicalToTime(times, 60, -2)).toBe(880);
    expect(logicalToTime(times, 60, 5)).toBe(1360);
  });

  it("round-trips whole indices", () => {
    for (let index = -3; index < 7; index++) {
      const time = logicalToTime(times, 60, index)!;
      expect(timeToLogical(times, 60, time)).toBe(index);
    }
  });

  it("returns null without bars", () => {
    expect(timeToLogical([], 60, 1000)).toBeNull();
    expect(logicalToTime([], 60, 0)).toBeNull();
  });
});

describe("geometry", () => {
  it("measures distance to a segment, clamped to its ends", () => {
    expect(distanceToSegment(5, 3, 0, 0, 10, 0)).toBe(3);
    expect(distanceToSegment(14, 3, 0, 0, 10, 0)).toBe(5);
    expect(distanceToSegment(1, 1, 0, 0, 0, 0)).toBeCloseTo(Math.SQRT2);
  });

  it("extends a ray to the pane edge in its direction", () => {
    expect(rayEnd(0, 0, 10, 5, 100)).toEqual([100, 50]);
    expect(rayEnd(50, 0, 40, 10, 100)).toEqual([0, 50]);
  });
});

describe("labels", () => {
  it("formats durations", () => {
    expect(duration(5 * 60)).toBe("5m");
    expect(duration(3600 + 30 * 60)).toBe("1h 30m");
    expect(duration(2 * 86400 + 3600)).toBe("2d 1h");
  });

  it("parses chart intervals", () => {
    expect(intervalSeconds("15m")).toBe(900);
    expect(intervalSeconds("4h")).toBe(14400);
    expect(intervalSeconds("1d")).toBe(86400);
  });
});
