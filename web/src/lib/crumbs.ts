// Breadcrumbs of a Settings page: Settings › group › the page's place under its sidebar item.
// PageHeader builds them; a page passes `crumbs` only when its place is not "sidebar item › title".

import { href } from "$lib/router.svelte";
import { navPlace, visibleNav } from "$lib/nav";

/** One step of a breadcrumb trail. The last step is the current page and has no href. */
export type Crumb = { label: string; href?: string };

/**
 * The trail for a Settings path. `title` is the page's h1. `tail` replaces what follows the group
 * (the repository settings pass "maven › Access").
 */
export function settingsTrail(path: string, title: string, tail?: Crumb[]): Crumb[] {
  const place = navPlace(path);
  const trail: Crumb[] = [{ label: "Settings", href: href("/settings") }];
  if (!place) return [...trail, ...(tail ?? [{ label: title }])];
  // The group links to the first item that the person may open.
  const first = visibleNav().find((g) => g.label === place.group.label)?.items[0]?.path ?? place.group.items[0].path;
  trail.push({ label: place.group.label, href: first === path ? undefined : href(first) });
  if (tail) return [...trail, ...tail];
  if (path === place.item.path) return [...trail, { label: place.item.label }];
  // The group already links to its first item, so a sub-page of that item does not name it again
  // (Settings › Repositories › New repository, as Settings › Repositories › maven › Access).
  if (place.item.path === first) return [...trail, { label: title }];
  return [...trail, { label: place.item.label, href: href(place.item.path) }, { label: title }];
}
