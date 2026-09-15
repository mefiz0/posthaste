import { createMockBackend } from "./mock";
import { hasWailsHost, WailsBackend } from "./wails";
import type { Backend } from "./backend";
import { AppService } from "../bindings/github.com/mefiz0/posthaste/internal/app/index.js";

/**
 * Where the app is running. "wails" is the real desktop engine; "mock" is the
 * in-browser sample-data backend used by `npm run dev`. Exposed for the debug
 * badge and for the few flows whose presentation differs (the mock cannot
 * open a real browser for OAuth).
 */
export const backendKind: "wails" | "mock" = hasWailsHost(
  typeof window === "undefined"
    ? undefined
    : (window as typeof window & { _wails?: { flags?: unknown } }),
)
  ? "wails"
  : "mock";

function selectBackend(): Backend {
  if (backendKind === "wails") return new WailsBackend();
  return createMockBackend();
}

/**
 * The only module components may use to reach the backend. Inside the Wails
 * webview this is the real Go engine; in a plain browser it is the mock, so
 * `npm run dev` keeps working without the engine.
 */
export const api: Backend = selectBackend();

/**
 * Opens a link in the user's default browser. Under Wails this routes through
 * the shell (scheme-validated); the mock logs. Links inside the message
 * iframe are additionally rewritten with target=_blank so the shell can
 * intercept navigation attempts that escape the sandbox.
 */
export function openExternal(url: string): void {
  if (backendKind === "wails") {
    void AppService.OpenExternal(url).catch(() => {
      console.error(`posthaste: could not open link: ${url}`);
    });
    return;
  }
  console.info(`posthaste: opening external link (mock): ${url}`);
}
