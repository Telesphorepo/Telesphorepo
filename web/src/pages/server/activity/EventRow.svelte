<!--
  One event of Activity: a row of the time of day, who acted, and the sentence with the facts it
  does not name after it (under it when they do not fit). The whole row is a button that opens
  the event's details under it, in place: every fact with its label, the exact time, the address
  (Activity is the only page that shows one), and quick filters for the person, the repository and
  the kind of event. The page keeps one row open at a time.

    <EventRow event={e} {action} open={openId === e.id} ontoggle={...} {formatOf} {filters} onfilter={...} />
-->
<script lang="ts">
  import type { AuditAction, AuditEvent } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import CopyButton from "$lib/components/CopyButton.svelte";
  import { formatDateTime } from "$lib/format";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import FunnelIcon from "@lucide/svelte/icons/funnel";
  import EventSentence from "./EventSentence.svelte";
  import EventActorIcon from "./EventActorIcon.svelte";
  import { actorOf, exactTime, refused, timeOfDay, twelveHour, wordEvent } from "./wording";

  type Filters = { actor: string; repo: string; action: string };

  let {
    event: e,
    action,
    open,
    ontoggle,
    formatOf,
    filters,
    onfilter,
  }: {
    event: AuditEvent;
    /** The event's action from GET /api/audit/actions (its label, sentence and fields). */
    action: AuditAction | undefined;
    open: boolean;
    ontoggle: () => void;
    formatOf: (repo: string) => string;
    /** The filters of the page, so a quick filter that is already on does not show. */
    filters: Filters;
    onfilter: (f: Partial<Filters>) => void;
  } = $props();

  const actor = $derived(actorOf(e));
  const worded = $derived(wordEvent(e, action));
  const target = $derived(e.target && e.target !== e.repo && e.target !== e.actor ? e.target : "");
  const timeWidth = twelveHour ? "w-16" : "w-11";
</script>

<li
  data-open={open ? "" : undefined}
  class={[
    "relative rounded-lg before:absolute before:inset-x-3 before:top-0 before:h-px before:bg-border first:before:hidden",
    open && "bg-panel",
  ]}
>
  <button
    type="button"
    class="flex w-full cursor-pointer items-start gap-3 rounded-lg px-3 py-2 text-left outline-none hover:bg-hover focus-visible:ring-[3px] focus-visible:ring-ring/50"
    aria-expanded={open}
    aria-controls="event-{e.id}"
    onclick={ontoggle}
  >
    <!-- On a phone the time moves under the sentence, so the sentence keeps the width. -->
    <time datetime={e.at} title={formatDateTime(e.at)} class={[timeWidth, "shrink-0 text-right text-meta leading-6 whitespace-nowrap text-muted-foreground tabular-nums max-sm:hidden"]}
      >{timeOfDay(e.at)}</time
    >
    <span class="flex h-6 shrink-0 items-center"><EventActorIcon {actor} /></span>
    <span class="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-3">
      <span class="min-w-0 text-body leading-6"><EventSentence parts={worded.sentence} {formatOf} /></span>
      {#if refused(e.action)}<span class="self-center"><StatusBadge tone="danger">Refused</StatusBadge></span>{/if}
      <span class={["min-w-0 text-meta text-muted-foreground max-sm:basis-full", !worded.facts.length && "sm:hidden"]}
        ><span class="tabular-nums sm:hidden">{timeOfDay(e.at)}{worded.facts.length ? " · " : ""}</span>{worded.facts.join(" · ")}</span
      >
    </span>
    <span class="flex h-6 shrink-0 items-center text-muted-foreground max-sm:hidden" aria-hidden="true">
      <ChevronDownIcon class={["size-4 transition-transform motion-reduce:transition-none", open && "rotate-180"]} />
    </span>
  </button>

  {#if open}
    <!-- The details line up with the sentence; on a phone they take the whole row. -->
    <div id="event-{e.id}" class="flex gap-3 px-3 pt-1 pb-4">
      <span class={[timeWidth, "shrink-0 max-sm:hidden"]}></span>
      <span class="w-6 shrink-0 max-sm:hidden"></span>
      <div class="flex min-w-0 flex-1 flex-col gap-3">
        <dl class="grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-6 gap-y-1.5">
          <dt class="text-meta text-muted-foreground">What</dt>
          <dd class="text-body">{action?.label ?? e.action}</dd>
          <dt class="text-meta text-muted-foreground">Who</dt>
          <dd class="flex min-w-0 items-center gap-2 text-body">
            {#if actor.kind === "person"}<UserAvatar name={actor.name} />{/if}
            <span class="min-w-0 [overflow-wrap:anywhere]">{actor.full}</span>
          </dd>
          {#if e.repo}
            <dt class="text-meta text-muted-foreground">Repository</dt>
            <dd class="text-body [overflow-wrap:anywhere]">
              {#if formatOf(e.repo)}<FormatIcon format={formatOf(e.repo)} class="mr-1 align-[-3px]" />{/if}{e.repo}
            </dd>
          {/if}
          {#if target}
            <dt class="text-meta text-muted-foreground">Applies to</dt>
            <dd class="flex min-w-0 items-start gap-1 text-body">
              <span class="min-w-0 [overflow-wrap:anywhere]">{target}</span>
              <CopyButton value={target} label="Copy {target}" size="icon-xs" class="-my-0.5" />
            </dd>
          {/if}
          {#each worded.details as f (f.key)}
            <dt class="text-meta text-muted-foreground">{f.label}</dt>
            <dd class="text-body [overflow-wrap:anywhere]">{f.value}</dd>
          {/each}
          <dt class="text-meta text-muted-foreground">When</dt>
          <dd class="text-body">{exactTime(e.at)} <span class="text-muted-foreground">· <RelativeTime at={e.at} /></span></dd>
          {#if e.ip}
            <dt class="text-meta text-muted-foreground">Address</dt>
            <dd class="flex min-w-0 items-start gap-1 text-body tabular-nums">
              <span class="min-w-0 [overflow-wrap:anywhere]">{e.ip}</span>
              <CopyButton value={e.ip} label="Copy the address" size="icon-xs" class="-my-0.5" />
            </dd>
          {/if}
        </dl>
        {#if (actor.kind === "person" && filters.actor !== actor.name) || (e.repo && filters.repo !== e.repo) || (action && filters.action !== e.action)}
          <div class="flex flex-wrap gap-2">
            {#if actor.kind === "person" && filters.actor !== actor.name}
              <Button variant="outline" size="sm" class="max-w-full" onclick={() => onfilter({ actor: actor.name })}><FunnelIcon /><span class="truncate">Only {actor.name}</span></Button>
            {/if}
            {#if e.repo && filters.repo !== e.repo}
              <Button variant="outline" size="sm" class="max-w-full" onclick={() => onfilter({ repo: e.repo })}><FunnelIcon /><span class="truncate">Only {e.repo}</span></Button>
            {/if}
            {#if action && filters.action !== e.action}
              <Button variant="outline" size="sm" class="max-w-full" onclick={() => onfilter({ action: e.action })}><FunnelIcon /><span class="truncate">Only “{action.label}”</span></Button>
            {/if}
          </div>
        {/if}
      </div>
      <span class="w-4 shrink-0 max-sm:hidden"></span>
    </div>
  {/if}
</li>
