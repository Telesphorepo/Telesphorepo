// A row that scrolls sideways (the tabs of TabNav, the tool tabs of the snippets): the end where
// items are cut off fades out, so the reader sees there is more. Use it as an action on the
// scrolling element: <nav use={fadeEdges} class="overflow-x-auto ...">.

const FADE = "2rem";

export function fadeEdges(el: HTMLElement) {
  const update = () => {
    const max = el.scrollWidth - el.clientWidth;
    const start = el.scrollLeft > 1;
    const end = el.scrollLeft < max - 1;
    el.style.maskImage =
      start && end
        ? `linear-gradient(to right, transparent, black ${FADE}, black calc(100% - ${FADE}), transparent)`
        : end
          ? `linear-gradient(to right, black calc(100% - ${FADE}), transparent)`
          : start
            ? `linear-gradient(to right, transparent, black ${FADE})`
            : "";
  };
  const ro = new ResizeObserver(update);
  ro.observe(el);
  for (const c of el.children) ro.observe(c);
  // Items that arrive later (tabs that follow loaded permissions) change the width without a resize.
  const mo = new MutationObserver(() => {
    for (const c of el.children) ro.observe(c);
    update();
  });
  mo.observe(el, { childList: true });
  el.addEventListener("scroll", update, { passive: true });
  update();
  return {
    destroy() {
      ro.disconnect();
      mo.disconnect();
      el.removeEventListener("scroll", update);
    },
  };
}
