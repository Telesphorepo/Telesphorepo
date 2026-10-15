// The one area every view of the Files tab fills (principle 11: switching views keeps the same
// area). Tree, Columns and Cover Flow are boxes of exactly this height; the List reserves it as
// its minimum height on wide screens, so the details column and the page around it never move.

/** The height of a view box: at least 22rem, at most 36rem, else what the window leaves. */
export const viewBox = "h-[max(22rem,min(36rem,calc(100svh-24rem)))]";

/** The same height as a minimum, for the List (wide screens only: on a phone the details follow). */
export const viewMin = "lg:min-h-[max(22rem,min(36rem,calc(100svh-24rem)))]";
