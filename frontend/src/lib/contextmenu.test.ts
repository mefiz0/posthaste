import { describe, expect, it } from "vitest";
import { clampMenuPosition } from "./contextmenu";

const VIEWPORT = { width: 1000, height: 800 };

describe("clampMenuPosition", () => {
  it("keeps a position that already fits", () => {
    expect(clampMenuPosition(100, 120, 3, VIEWPORT)).toEqual({
      x: 100,
      y: 120,
    });
  });

  it("pulls the menu back from the right edge", () => {
    // maxX = 1000 - 220 - 8.
    expect(clampMenuPosition(990, 120, 3, VIEWPORT)).toEqual({
      x: 772,
      y: 120,
    });
  });

  it("pulls the menu up from the bottom edge", () => {
    // height = 3 * 32 + 8, so maxY = 800 - 104 - 8.
    expect(clampMenuPosition(100, 900, 3, VIEWPORT)).toEqual({
      x: 100,
      y: 688,
    });
  });

  it("keeps a margin when the point is off-screen", () => {
    expect(clampMenuPosition(-40, -40, 2, VIEWPORT)).toEqual({ x: 8, y: 8 });
  });
});
