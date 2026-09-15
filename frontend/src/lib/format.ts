const MS_PER_DAY = 86_400_000;

const WEEKDAYS_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const WEEKDAYS_LONG = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];
const MONTHS_SHORT = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];

function startOfDay(date: Date): number {
  return new Date(
    date.getFullYear(),
    date.getMonth(),
    date.getDate(),
  ).getTime();
}

function isSameCalendarDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

function pad2(value: number): string {
  return String(value).padStart(2, "0");
}

function clockTime(date: Date): string {
  return `${pad2(date.getHours())}:${pad2(date.getMinutes())}`;
}

/** List timestamp: Today -> HH:MM, Yesterday, weekday within a week, else "Aug 28". */
export function formatListTime(date: Date, now: Date = new Date()): string {
  if (isSameCalendarDay(date, now)) return clockTime(date);
  const yesterday = new Date(now.getTime() - MS_PER_DAY);
  if (isSameCalendarDay(date, yesterday)) return "Yesterday";
  const dayDistance = Math.round(
    (startOfDay(now) - startOfDay(date)) / MS_PER_DAY,
  );
  if (dayDistance >= 0 && dayDistance < 7)
    return WEEKDAYS_SHORT[date.getDay()] ?? "";
  return `${MONTHS_SHORT[date.getMonth()] ?? ""} ${date.getDate()}`;
}

/** Full timestamp for the reading pane: "Today, 14:32" / "Monday, 22:05" / "Aug 28, 2025". */
export function formatDateFull(date: Date, now: Date = new Date()): string {
  if (isSameCalendarDay(date, now)) return `Today, ${clockTime(date)}`;
  const yesterday = new Date(now.getTime() - MS_PER_DAY);
  if (isSameCalendarDay(date, yesterday))
    return `Yesterday, ${clockTime(date)}`;
  const dayDistance = Math.round(
    (startOfDay(now) - startOfDay(date)) / MS_PER_DAY,
  );
  if (dayDistance >= 0 && dayDistance < 7) {
    return `${WEEKDAYS_LONG[date.getDay()] ?? ""}, ${clockTime(date)}`;
  }
  const monthDay = `${MONTHS_SHORT[date.getMonth()] ?? ""} ${date.getDate()}`;
  if (date.getFullYear() !== now.getFullYear())
    return `${monthDay}, ${date.getFullYear()}`;
  return monthDay;
}

/** Binary-unit byte sizes: "840 KB", "2.4 MB", "1.2 GB". */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  if (bytes < 1024 * 1024 * 1024) {
    const mb = bytes / (1024 * 1024);
    return `${Number.isInteger(mb) ? String(mb) : mb.toFixed(1)} MB`;
  }
  const gb = bytes / (1024 * 1024 * 1024);
  return `${Number.isInteger(gb) ? String(gb) : gb.toFixed(1)} GB`;
}

/** Up to two leading initials, uppercased: "Sarah Chen" -> "SC". */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const first = parts[0]?.[0] ?? "";
  const second = parts[1]?.[0] ?? "";
  return (first + second).toUpperCase();
}

/** Strips repeated reply/forward markers: "Re: Re: Fwd: x" -> "x". */
export function normalizeSubject(subject: string): string {
  let current = subject.trim();
  let previous = "";
  while (current && current !== previous) {
    previous = current;
    current = current
      .replace(/^(?:re|fwd?|aw|tr)\s*(?:\[\d+])?\s*:\s*/i, "")
      .trim();
  }
  return current;
}

export function replySubject(subject: string): string {
  return `Re: ${normalizeSubject(subject)}`;
}

export function forwardSubject(subject: string): string {
  return `Fwd: ${normalizeSubject(subject)}`;
}
