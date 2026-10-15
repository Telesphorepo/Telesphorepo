// The route of the current address, for the shell (App.svelte) and the parts of a page that depend
// on where it sits: the breadcrumbs (PageHeader) and the placeholder page.

import { match, redirectOf, router } from "$lib/router.svelte";
import { redirects, routes } from "../routes";

class Current {
  /** The path that the current path redirects to, or null. */
  redirect = $derived(redirectOf(redirects, router.path));
  /** The matched route and its params, or null (no such page). */
  match = $derived(match(routes, router.path));
  route = $derived(this.match?.route ?? null);
}

export const current = new Current();
