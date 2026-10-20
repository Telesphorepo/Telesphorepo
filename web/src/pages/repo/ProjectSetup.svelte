<!--
  Set up a project with the Telesphorepo CLI. The Set up a project button asks for a fresh second
  factor (a single sign-on account signs in again at its identity provider and returns with
  ?setup_grant=), then POST /api/publish/setup-codes makes a single-use setup code that is valid
  for 10 minutes. The page shows the install line for the OS of the browser, with a toggle for the
  other OS. The line carries the link <server>/#/setup/<code>. The code sits in the fragment, so
  no request ever sends it to the server. A format of the "cli" publish category then publishes
  with telesphorepo publish; a format of the "helper" category with its native command. The manual
  snippets of the format stay behind one "Set it up by hand" disclosure.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { api, errorMessage, type FreshProof, type Repo, type SetupCode, type Snippet } from "$lib/api";
  import { auth } from "$lib/auth.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import { href, navigate, repoPath } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import Section from "$lib/components/Section.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import FreshFactor from "$lib/components/FreshFactor.svelte";
  import LearnMore from "$lib/components/LearnMore.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import Segmented from "$lib/components/Segmented.svelte";
  import SnippetTabs from "./snippets/SnippetTabs.svelte";
  import { ssoErrors } from "../account/words";
  import FolderCogIcon from "@lucide/svelte/icons/folder-cog";
  import TerminalIcon from "@lucide/svelte/icons/terminal";
  import type { ToolLabel } from "$lib/api";

  let {
    repo,
    baseURL,
    computer,
    tools,
    query,
  }: { repo: Repo; baseURL: string; computer: Snippet[]; tools: ToolLabel[]; query: URLSearchParams } = $props();

  const helper = $derived(formats.get(repo.format)?.publish === "helper");
  // The native commands that publish from a computer, e.g. "docker push" and "helm push".
  const runs = $derived([...new Set(computer.filter((s) => !s.file && s.lang === "sh").map((s) => s.code.trim()))]);

  const sso = $derived(auth.user?.kind === "oidc");
  let os = $state<"unix" | "windows">(typeof navigator !== "undefined" && /Windows/i.test(navigator.userAgent) ? "windows" : "unix");
  let asking = $state(false);
  let busy = $state(false);
  let created = $state<SetupCode | null>(null);
  let error = $state(untrack(() => (query.has("error") ? (ssoErrors[query.get("error")!] ?? ssoErrors.sso_failed) : "")));
  let byHand = $state(false);

  const base = $derived(baseURL.replace(/\/+$/, ""));
  const line = $derived(
    created
      ? os === "windows"
        ? `& ([scriptblock]::Create((irm ${base}/install.ps1))) setup '${created.link}'`
        : `curl -fsSL ${base}/install.sh | sh -s -- setup '${created.link}'`
      : "",
  );
  const uid = $props.id();

  async function create(proof: FreshProof) {
    created = await api.publish.setupCode(repo.name, proof);
    asking = false;
  }

  // A single sign-on account returns from its identity provider with a grant: the code is made at once.
  const grant = untrack(() => query.get("setup_grant") ?? "");
  if (grant || untrack(() => query.has("error"))) {
    // Keep the grant and the error out of the address: the grant works once.
    navigate(untrack(() => repoPath(repo.name, "publish")), { replace: true });
  }
  if (grant) {
    busy = true;
    create({ grant })
      .catch((e) => (error = errorMessage(e)))
      .finally(() => (busy = false));
  }
</script>

<Section
  title="Set up a project"
  description={helper
    ? "Run one line in a terminal. It signs this computer in and adds the credential helper for the native command."
    : "Run one line in the project folder. It signs this computer in and writes telesphorepo.toml."}
>
  {#snippet actions()}
    <Button variant="ghost" href={href("/install")}><TerminalIcon />Install the CLI</Button>
  {/snippet}
  {#if created}
    <div class="flex flex-col gap-4">
      <p class="max-w-prose text-body text-muted-foreground">
        Run this line in a terminal{helper ? "" : " in the project folder"}. It works once and ends <RelativeTime at={created.expires_at} />.
      </p>
      <Segmented
        bind:value={os}
        label="Operating system"
        options={[
          { value: "unix", label: "macOS or Linux" },
          { value: "windows", label: "Windows" },
        ]}
      />
      <CodeSnippet label={os === "windows" ? "PowerShell" : "Terminal"} value={line} />
      <div class="flex flex-col gap-2">
        {#if helper}
          <p class="max-w-prose text-body text-muted-foreground">Then publish with the native command{runs.length > 1 ? "s" : ""} of the tool.</p>
          {#each runs as run (run)}<CodeSnippet value={run} />{/each}
        {:else}
          <p class="max-w-prose text-body text-muted-foreground">Then publish from the project folder.</p>
          <CodeSnippet value="telesphorepo publish" />
        {/if}
      </div>
      <div>
        <Button variant="outline" onclick={() => (created = null)}>Make another line</Button>
      </div>
    </div>
  {:else if busy}
    <p class="flex items-center gap-2 text-body text-muted-foreground"><Spinner />Making the setup line</p>
  {:else if asking}
    <div class="flex flex-col items-start gap-4">
      {#if sso}
        <p class="max-w-prose text-body">Your identity provider asks you to sign in again. Then this page shows the line.</p>
        <Button href={api.publish.ssoSetupURL(repo.name)}>Sign in again with single sign-on</Button>
      {:else}
        <FreshFactor actionLabel="Set up a project" onProof={create} back={repoPath(repo.name, "publish")} />
      {/if}
      <Button variant="ghost" onclick={() => (asking = false)}>Cancel</Button>
    </div>
  {:else}
    <div class="flex flex-col items-start gap-3">
      <Button onclick={() => (asking = true)}><FolderCogIcon />Set up a project</Button>
      {#if error}<ErrorState inline {error} />{/if}
    </div>
  {/if}
  {#if computer.length}
    <div class="mt-6 flex flex-col">
      <LearnMore bind:open={byHand} label="Set it up by hand" toggleOnly controls="{uid}-hand" />
      {#if byHand}
        <div id="{uid}-hand" class="mt-4">
          <SnippetTabs snippets={computer} {tools} label="Build tool" />
        </div>
      {/if}
    </div>
  {/if}
</Section>