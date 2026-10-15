// The links to a repository's Access tab, for the pages that list the repositories a network list
// or a CI pipeline reaches.

import { auth } from "$lib/auth.svelte";
import { href, repoPath, repoSettingsPath } from "$lib/router.svelte";

/** The address of a repository's Access tab, or of its files when the person cannot open the tab. */
export const repoAccessHref = (repo: string) => href(auth.managesRepo(repo) ? repoSettingsPath(repo, "access") : repoPath(repo));
