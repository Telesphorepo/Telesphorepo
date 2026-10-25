// Breadcrumbs of a page with a sidebar: Settings › group › the page's place under its sidebar item,
// or Your account › the page. PageHeader builds them; a page passes `crumbs` only when its place is
// not "sidebar item › title". The groups and items come from the UI modules (web/src/modules).

import { href } from "$lib/router.svelte";
import { auth } from "$lib/auth.svelte";
import { areaOf, navPlace, visibleNav } from "../modules";

/** One step of a breadcrumb trail. The last step is the current page and has no href. */
export type Crumb = { label: string; href?: string };

/**
 * The trail for a Settings or account path. `title` is the page's h1. `tail` replaces what follows
 * the group (the repository settings pass "maven › Access").
 */
export function sidebarTrail(path: string, title: string, tail?: Crumb[]): Crumb[] {
  const place = navPlace(path);
  // The address of All settings itself, so its own page knows the step is itself.
  const settings: Crumb = { label: "Settings", href: href("/settings/overview") };
  if (!place) return [settings, ...(tail ?? [{ label: title }])];
  const area = areaOf(place.group.id);
  // The group links to the first item that the person may open.
  const first =
    visibleNav(area, (p) => auth.allows(p.access)).find((g) => g.id === place.group.id)?.items[0]?.path ?? place.group.items[0].path;
  const group: Crumb = { label: place.group.label, href: first === path ? undefined : href(first) };
  // Your account is its own area: its group is the first step (Your account › Devices).
  const trail: Crumb[] = area === "account" ? [group] : [settings, group];
  if (tail) return [...trail, ...tail];
  const label = place.item.nav?.label ?? place.item.title;
  // An item named like its group is that step (Settings › Repositories, not Settings › Repositories › Repositories).
  if (label === place.group.label) {
    if (path === place.item.path) return [settings, { label }];
    return [settings, { label, href: href(place.item.path) }, { label: title }];
  }
  if (path === place.item.path) return [...trail, { label }];
  // The group already links to its first item, so a sub-page of that item does not name it again
  // (Settings › Repositories › New repository, as Settings › Repositories › maven › Access).
  if (place.item.path === first) return [...trail, { label: title }];
  return [...trail, { label, href: href(place.item.path) }, { label: title }];
}
