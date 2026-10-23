<!--
  The admission ticket of a CI pipeline: what the form says, as a ticket that fills in while the
  person edits. The top part admits the CI system and the jobs (a GitHub repository with its
  branches, tags and workflow file, a Jenkins address with its jobs, or the token conditions);
  the stub under the perforation names the repositories it admits them to. A notch cuts into
  each side where the two parts meet, as on the mark.

  It is at most 24rem wide, so on a phone it keeps the size of a ticket. The notches are holes:
  the surface behind it shows through them.
-->
<script lang="ts">
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import TicketBadge from "$lib/components/TicketBadge.svelte";
  import Perforation from "$lib/components/Perforation.svelte";
  import { cn } from "$lib/utils";
  import ConditionBadges from "./ConditionBadges.svelte";
  import { admitsOf, ciSystemTitle, gitHubAccepts, hostOf, jenkinsURL, systemTitle, type Accepts, type GitHubJobs, type JenkinsJobs, type System } from "./presets";

  let {
    system,
    hand,
    gh,
    jk,
    accepts,
    repos,
    formatOf,
  }: {
    system: System;
    /** The token conditions show in place of the answers. */
    hand: boolean;
    gh: GitHubJobs;
    jk: JenkinsJobs;
    /** What the pipeline accepts: read when `hand`. */
    accepts: Accepts;
    /** The chosen repositories. */
    repos: string[];
    formatOf: (repo: string) => string | undefined;
  } = $props();

  // The answers read back as the cards read a stored pipeline.
  const admits = $derived.by(() => {
    if (!hand && system === "github") return { ci: systemTitle.github, ...admitsOf(gitHubAccepts(gh)) };
    if (!hand && system === "jenkins")
      return {
        ci: systemTitle.jenkins,
        who: jk.url.trim() ? hostOf(jenkinsURL(jk.url)) : "",
        items: jk.jobs.map((value) => ({ kind: "job" as const, value, label: "Job" })),
        every: false,
      };
    return { ci: system === "other" && !accepts.issuer ? systemTitle.other : ciSystemTitle(accepts.issuer), ...admitsOf(accepts) };
  });
  const placeholder = $derived(hand ? "" : system === "github" ? "Your GitHub repository" : system === "jenkins" ? "Your Jenkins" : "");

  let top = $state(0);
</script>

<div class="ticket relative w-full max-w-sm text-body" style:--seam="{top + 1.5}px" role="group" aria-label="What this CI pipeline admits">
  <div class="edge absolute inset-0 rounded-lg bg-border" aria-hidden="true"></div>
  <div class="paper absolute inset-px rounded-[calc(var(--radius)-1px)] bg-ticket-paper" aria-hidden="true"></div>

  <div class="relative flex flex-col gap-3 px-5 pt-5 pb-4" bind:clientHeight={top}>
    <div class="flex items-center justify-between gap-3">
      <p class="text-meta text-muted-foreground">Admits</p>
      <TicketBadge tone="gold" class="max-w-[60%]" title={admits.ci}>{admits.ci}</TicketBadge>
    </div>
    {#if admits.who || placeholder}
      <p class={cn("truncate text-section", !admits.who && "font-normal text-muted-foreground")} title={admits.who}>{admits.who || placeholder}</p>
    {/if}
    {#if admits.items.length || admits.every}
      <ConditionBadges items={admits.items} every={admits.every} />
    {:else if hand}
      <p class="text-meta text-muted-foreground">No condition yet</p>
    {/if}
  </div>

  <div class="relative px-4"><Perforation class="text-foreground/15" /></div>

  <div class="relative flex flex-col gap-2 px-5 pt-4 pb-5">
    <p class="text-meta text-muted-foreground">To</p>
    {#if repos.length}
      <ul class="flex flex-col gap-1.5" aria-label="Repositories">
        {#each repos as r (r)}
          {@const f = formatOf(r)}
          <li class="flex min-w-0 items-center gap-2 text-label">
            {#if f}<FormatIcon format={f} class="text-muted-foreground" />{/if}
            <span class="truncate">{r}</span>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="text-muted-foreground">No repository yet</p>
    {/if}
  </div>
</div>

<style>
  /* The notches: a hole of --r at each side of the seam. The edge layer is cut a pixel smaller
     than the paper, so the border runs round the notch. */
  .ticket {
    --r: 10px;
    filter: drop-shadow(0 1px 2px var(--ticket-shadow)) drop-shadow(0 6px 16px var(--ticket-shadow));
  }
  .edge {
    mask:
      radial-gradient(circle var(--r) at 0 var(--seam), #0000 calc(var(--r) - 0.5px), #000 var(--r)) 0 0 / 51% 100% no-repeat,
      radial-gradient(circle var(--r) at 100% var(--seam), #0000 calc(var(--r) - 0.5px), #000 var(--r)) 100% 0 / 51% 100% no-repeat;
  }
  .paper {
    --p: calc(var(--r) + 1px);
    mask:
      radial-gradient(circle var(--p) at -1px calc(var(--seam) - 1px), #0000 calc(var(--p) - 0.5px), #000 var(--p)) 0 0 / 51% 100% no-repeat,
      radial-gradient(circle var(--p) at calc(100% + 1px) calc(var(--seam) - 1px), #0000 calc(var(--p) - 0.5px), #000 var(--p)) 100% 0 / 51% 100% no-repeat;
  }
</style>
