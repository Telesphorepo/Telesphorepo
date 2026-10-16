// The page of the current address, for the shell (App.svelte) and the parts of a page that depend
// on where it sits: the breadcrumbs (PageHeader) and the placeholder page. The pages and the
// redirects come from the UI modules (web/src/modules).

import { match, redirectOf, router } from "$lib/router.svelte";
import { pages, redirects } from "../modules";

class Current {
  /** The path that the current path redirects to, or null. */
  redirect = $derived(redirectOf(redirects, router.path));
  /** The matched page and its params, or null (no such page). */
  match = $derived(match(pages, router.path));
  route = $derived(this.match?.route ?? null);
}

export const current = new Current();
