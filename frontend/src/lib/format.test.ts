import { describe, expect, it } from "vitest";
import {
  formatBytes,
  formatDateFull,
  formatListTime,
  initials,
  normalizeSubject,
  forwardSubject,
  replySubject,
} from "./format";

const NOW = new Date(2026, 8, 14, 12, 0); // Monday, Sep 14 2026, 12:00 local

function daysAgo(days: number, hours = 9, minutes = 30): Date {
  const date = new Date(NOW);
  date.setDate(date.getDate() - days);
  date.setHours(hours, minutes, 0, 0);
  return date;
}

describe("formatListTime", () => {
  it("shows HH:MM for today", () => {
    expect(formatListTime(new Date(2026, 8, 14, 14, 32), NOW)).toBe("14:32");
  });

  it("shows Yesterday for the previous calendar day", () => {
    expect(formatListTime(daysAgo(1, 17, 20), NOW)).toBe("Yesterday");
  });

  it("shows the weekday within the past week", () => {
    expect(formatListTime(daysAgo(4, 22, 5), NOW)).toBe("Thu");
  });

  it("shows month and day beyond a week", () => {
    expect(formatListTime(daysAgo(17, 11, 8), NOW)).toBe("Aug 28");
  });

  it("does not treat 24h ago inside the same calendar day as yesterday", () => {
    // NOW is 12:00; 30 hours back is yesterday 06:00.
    const date = new Date(NOW.getTime() - 30 * 3600 * 1000);
    expect(formatListTime(date, NOW)).toBe("Yesterday");
  });
});

describe("formatDateFull", () => {
  it("formats today with clock time", () => {
    expect(formatDateFull(new Date(2026, 8, 14, 14, 32), NOW)).toBe(
      "Today, 14:32",
    );
  });

  it("formats weekday with clock time within the week", () => {
    expect(formatDateFull(daysAgo(4, 22, 5), NOW)).toBe("Thursday, 22:05");
  });

  it("adds the year for a different year", () => {
    expect(formatDateFull(new Date(2025, 7, 28, 10, 0), NOW)).toBe(
      "Aug 28, 2025",
    );
  });
});

describe("formatBytes", () => {
  it("formats bytes", () => {
    expect(formatBytes(512)).toBe("512 B");
  });

  it("formats kilobytes without decimals", () => {
    expect(formatBytes(840 * 1024)).toBe("840 KB");
  });

  it("formats megabytes with one decimal", () => {
    expect(formatBytes(2.4 * 1024 * 1024)).toBe("2.4 MB");
  });

  it("drops a trailing .0 on megabytes", () => {
    expect(formatBytes(2 * 1024 * 1024)).toBe("2 MB");
  });

  it("formats gigabytes", () => {
    expect(formatBytes(1.5 * 1024 * 1024 * 1024)).toBe("1.5 GB");
  });

  it("handles zero", () => {
    expect(formatBytes(0)).toBe("0 B");
  });
});

describe("initials", () => {
  it("takes two leading initials uppercased", () => {
    expect(initials("Sarah Chen")).toBe("SC");
  });

  it("handles single names", () => {
    expect(initials("Linear")).toBe("L");
  });

  it("collapses whitespace", () => {
    expect(initials("  anna   kowalski ")).toBe("AK");
  });

  it("returns empty for empty input", () => {
    expect(initials("")).toBe("");
  });
});

describe("subject normalization", () => {
  it("strips repeated reply markers", () => {
    expect(normalizeSubject("Re: Re: Fwd: deployment")).toBe("deployment");
  });

  it("keeps a subject that has no markers", () => {
    expect(normalizeSubject("Lunch next week?")).toBe("Lunch next week?");
  });

  it("builds reply subjects without stacking Re:", () => {
    expect(replySubject("Re: Project proposal")).toBe("Re: Project proposal");
  });

  it("builds forward subjects", () => {
    expect(forwardSubject("Contract renewal")).toBe("Fwd: Contract renewal");
  });
});
