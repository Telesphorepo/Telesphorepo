// The one area every view of the Files tab fills (principle 11: switching views keeps the same
// area). Tree, Columns and Cover Flow are boxes of exactly this height: the window under the
// repository's header and the panel's toolbar, at least 22rem. The List reserves it as its minimum
// height on wide screens, and the file pane beside the view is as tall and scrolls inside itself,
// so the view and the pane line up and the page around them never moves.

/** The height of a view box: what the window leaves under the header and the toolbar, at least 22rem. */
export const viewBox = "h-[max(22rem,calc(100svh-19rem))]";

/** The same height as a minimum, for the List (wide screens only: on a phone the pane follows). */
export const viewMin = "lg:min-h-[max(22rem,calc(100svh-19rem))]";

/** The file pane on wide screens: the view box and its padding (0.5rem above, 0.75rem under). */
export const paneBox = "lg:h-[calc(max(22rem,calc(100svh-19rem))+1.25rem)] lg:overflow-y-auto";
