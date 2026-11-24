// A row that scrolls sideways (the tabs of TabNav, the tool tabs of the snippets), or with "y" an
// area that scrolls down (the repositories of a CI pipeline): the end where items are cut off fades
// out, so the reader sees there is more. Use it as an action on the scrolling element:
// <nav use:fadeEdges class="overflow-x-auto ...">, <div use:fadeEdges={"y"} class="overflow-y-auto ...">.

const FADE = "2rem";

export function fadeEdges(el: HTMLElement, axis: "x" | "y" = "x") {
  const update = () => {
    const y = axis === "y";
    const max = y ? el.scrollHeight - el.clientHeight : el.scrollWidth - el.clientWidth;
    const at = y ? el.scrollTop : el.scrollLeft;
    const start = at > 1;
    const end = at < max - 1;
    const to = y ? "to bottom" : "to right";
    el.style.maskImage =
      start && end
        ? `linear-gradient(${to}, transparent, black ${FADE}, black calc(100% - ${FADE}), transparent)`
        : end
          ? `linear-gradient(${to}, black calc(100% - ${FADE}), transparent)`
          : start
            ? `linear-gradient(${to}, transparent, black ${FADE})`
            : "";
  };
  // A tab can change its width on its own; the rows of an area that scrolls down change its height
  // only by coming and going, which the MutationObserver sees, so they are not watched one by one.
  const ro = new ResizeObserver(update);
  const watch = () => {
    if (axis === "x") for (const c of el.children) ro.observe(c);
  };
  ro.observe(el);
  watch();
  // Items that arrive later (tabs that follow loaded permissions, the next page of a list) change
  // the size without a resize.
  const mo = new MutationObserver(() => {
    watch();
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
