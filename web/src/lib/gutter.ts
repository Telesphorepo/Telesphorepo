// A dialog locks the page with overflow: hidden on body (bits-ui), which the browser moves to the
// viewport. The root reserves its scrollbar gutter (app.css), so bits-ui adds no padding. But
// where the system's scrollbars overlay the page (macOS), Chrome keeps no gutter on a viewport
// with overflow: hidden, even with the app's scrollbar styles, so the page widens and shifts.
// measureLockGutter sets --lock-gutter on the root to the width the viewport then loses, and
// app.css pads the root by it while a dialog is open. Elsewhere it is not set.

/** The width of the vertical scrollbar of a probe element with the given extra style. */
function scrollbarWidth(style: string): number {
  const d = document.createElement("div");
  d.style.cssText = `position:absolute;top:-9999px;width:100px;height:50px;overflow:scroll;${style}`;
  document.documentElement.appendChild(d);
  const w = d.offsetWidth - d.clientWidth;
  d.remove();
  return w;
}

/** Sets --lock-gutter when the system's scrollbars overlay the page and the app's take room. */
export function measureLockGutter(): void {
  const styled = scrollbarWidth(""); // the app's ::-webkit-scrollbar width
  // A scrollbar-width other than auto makes Chrome ignore ::-webkit-scrollbar: the system's own.
  const system = scrollbarWidth("scrollbar-width:thin");
  if (system === 0 && styled > 0) document.documentElement.style.setProperty("--lock-gutter", `${styled}px`);
}
