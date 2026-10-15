// The links to a repository's Access tab, for the pages that list the repositories a network list
// or a CI pipeline reaches.

import { href, repoSettingsPath } from "$lib/router.svelte";

/** The address of a repository's Access tab. */
export const repoAccessHref = (repo: string) => href(repoSettingsPath(repo, "access"));
