<!--
  Activity (activity.read): what happened on the server, newest first, in one page with two
  views. Events (the audit log) filter by what happened, by repository, by person and by time
  (?area=&action=&repo=&actor=&when=&from=&to=): first the area (the subsystem that registers the
  action), then one action of it, a repository picker, a person picker (GET /api/audit/people) and
  a date range (Today, Last 7 days, Last 30 days, or Custom: from one day to another, both
  included, in the reader's time zone); the active filters show as chips. The events are grouped by day, each a list
  of who did what, the facts in words, and when; this is the only page that shows IP addresses.
  Publish tokens (?view=publishing) lists every publish token that a device, a CI job or a person
  got (the publish log).
-->
<script lang="ts">
  import { untrack, type Component } from "svelte";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate, router } from "$lib/router.svelte";
  import { api, type AuditEvent, type SessionInfo } from "$lib/api";
  import { Keyset, RepoFormats, Resource } from "$lib/load.svelte";
  import { subsystems } from "$lib/permissions";
  import { navPlace, pages } from "../../modules";
  import { detailFacts } from "./activity/details";
  import FunnelXIcon from "@lucide/svelte/icons/funnel-x";
  import * as Select from "$lib/components/ui/select";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import TabNav from "$lib/components/TabNav.svelte";
  import Section from "$lib/components/Section.svelte";
  import Toolbar from "$lib/components/Toolbar.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import Logo from "$lib/components/Logo.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import RepoPicker from "$lib/components/RepoPicker.svelte";
  import PersonPicker from "$lib/components/PersonPicker.svelte";
  import { Input } from "$lib/components/ui/input";
  import { bounds, dayValue, parseWhen, rangeLabel, ranges, type When } from "./activity/range";
  import CalendarIcon from "@lucide/svelte/icons/calendar";
  import FilterChip from "$lib/components/FilterChip.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SourceError from "$lib/components/SourceError.svelte";
  import SourceMore from "$lib/components/SourceMore.svelte";
  import HistoryIcon from "@lucide/svelte/icons/history";
  import UploadIcon from "@lucide/svelte/icons/upload";

  let { query }: PageProps = $props();

  type Area = { id: string; label: string; icon?: Component<{ class?: string }>; actions: { id: string; label: string }[] };

  // The icon of an area: the sidebar icon over the Settings page that one of its permissions
  // opens; an area of repository permissions takes the one over a repository's settings.
  function iconOf(perms: string[], scope: string): Area["icon"] {
    const page = pages.find((p) => p.layout === "settings" && ((typeof p.access === "string" && perms.includes(p.access)) || (scope === "repo" && p.access === "repo")));
    return page ? navPlace(page.path)?.item.nav?.icon : undefined;
  }

  // The actions that the modules register (GET /api/audit/actions), grouped by the permission
  // subsystem they belong to, in registry order.
  const actions = new Resource(() => api.auditActions());
  const areas = $derived.by(() => {
    const out: Area[] = [];
    for (const sub of subsystems()) {
      const list = (actions.data ?? []).filter((a) => a.subsystem === sub.id);
      if (list.length) out.push({ id: sub.id, label: sub.label, icon: iconOf(sub.permissions.map((p) => p.id), sub.scope), actions: list });
    }
    return out;
  });
  const actionText = (a: string) => actions.data?.find((x) => x.id === a)?.label ?? a;
  const areaOfAction = (a: string) => areas.find((x) => x.actions.some((y) => y.id === a));
  // Refusals show in red.
  const refusals = new Set(["signin.failed", "sso.signin.failed", "network.denied"]);

  const view = $derived(query.get("view") === "publishing" ? "publishing" : "events");
  const action = $derived(query.get("action") ?? "");
  const repo = $derived(query.get("repo") ?? "");
  const area = $derived(areas.find((a) => a.id === query.get("area")) ?? (action ? areaOfAction(action) : undefined));
  const actor = $derived(query.get("actor") ?? "");
  const when = $derived(parseWhen(query.get("when")));
  const from = $derived(when === "custom" ? (query.get("from") ?? "") : "");
  const to = $derived(when === "custom" ? (query.get("to") ?? "") : "");
  const filtered = $derived(action !== "" || repo !== "" || actor !== "" || when !== "");

  // The format icons of the repositories the events and the filter name, read for those names only.
  const repoFormats = new RepoFormats();
  const formatOf = (name: string) => repoFormats.format(name);

  const events = new Keyset((p) =>
    api.audit({ action: action || undefined, repo: repo || undefined, actor: actor || undefined, ...bounds(when, from, to), ...p }),
  );
  const sessions = new Keyset((p) => api.publish.log(p));
  $effect(() => repoFormats.want([repo, ...events.items.map((e) => e.repo ?? "")]));

  // A filter change loads the first page again; the first run is the page's own load.
  let first = true;
  $effect(() => {
    void action;
    void repo;
    void actor;
    void when;
    void from;
    void to;
    if (first) {
      first = false;
      return;
    }
    untrack(() => events.reload());
  });

  type Filters = { area?: string; action?: string; repo?: string; actor?: string; when?: When; from?: string; to?: string };
  function setFilter(next: Filters) {
    const ar = next.area ?? area?.id ?? "";
    const a = next.action ?? action;
    const r = next.repo ?? repo;
    const who = next.actor ?? actor;
    const w = next.when ?? when;
    const f = w === "custom" ? (next.from ?? from) : "";
    const t = w === "custom" ? (next.to ?? to) : "";
    navigate(router.path, {
      query: {
        area: ar || undefined,
        action: a || undefined,
        repo: r || undefined,
        actor: who || undefined,
        when: w || undefined,
        from: f || undefined,
        to: t || undefined,
      },
      replace: true,
    });
  }
  // Custom starts with the last 7 days, so the two days show what it means.
  function pickWhen(v: string) {
    const w = v === "any" ? "" : parseWhen(v);
    if (w === "custom" && when !== "custom") {
      const today = new Date();
      setFilter({ when: w, from: dayValue(new Date(today.getFullYear(), today.getMonth(), today.getDate() - 6)), to: dayValue(today) });
    } else setFilter({ when: w });
  }
  const clear = () => navigate(router.path, { replace: true });

  // The events by the day they happened on, in the reader's time zone.
  const dayKey = (iso: string) => {
    const d = new Date(iso);
    return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
  };
  const weekday = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long" });
  const withYear = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" });
  function dayLabel(iso: string): string {
    const d = new Date(iso);
    const today = new Date();
    const yesterday = new Date(today.getFullYear(), today.getMonth(), today.getDate() - 1);
    if (dayKey(iso) === dayKey(today.toISOString())) return "Today";
    if (dayKey(iso) === dayKey(yesterday.toISOString())) return "Yesterday";
    return (d.getFullYear() === today.getFullYear() ? weekday : withYear).format(d);
  }
  const days = $derived.by(() => {
    const out: { key: string; label: string; source: { items: AuditEvent[]; loading: boolean; error: string } }[] = [];
    for (const e of events.items) {
      const k = dayKey(e.at);
      let day = out[out.length - 1];
      if (!day || day.key !== k) out.push((day = { key: k, label: dayLabel(e.at), source: { items: [], loading: false, error: "" } }));
      day.source.items.push(e);
    }
    return out;
  });

  function who(s: SessionInfo): string {
    if (s.via === "oidc") return `${s.trust_name ?? "a deleted pipeline"}: ${s.subject}`;
    if (s.via === "device") return `${s.username ?? "a deleted account"} on ${s.device_name ?? "a deleted device"}`;
    return s.username ?? "a deleted account";
  }
</script>

<PageHeader title="Activity" />

<TabNav
  label="Activity"
  items={[
    { label: "Events", href: href("/settings/activity"), active: view === "events", icon: HistoryIcon },
    { label: "Publish tokens", href: href("/settings/activity", { view: "publishing" }), active: view === "publishing", icon: UploadIcon },
  ]}
/>

{#snippet areaIcon(a: Area | undefined)}{#if a?.icon}<a.icon class="size-4" />{/if}{/snippet}
{#snippet chosenArea()}{@render areaIcon(area)}{/snippet}
{#snippet repoIcon()}{#if formatOf(repo)}<FormatIcon format={formatOf(repo)} />{/if}{/snippet}
{#snippet actorIcon()}<UserAvatar name={actor} />{/snippet}
{#snippet calendarIcon()}<CalendarIcon class="size-4" />{/snippet}

<Section>
{#if view === "events"}
  <Toolbar>
    <Select.Root type="single" value={area?.id ?? "all"} onValueChange={(v) => setFilter({ area: v === "all" ? "" : v, action: "" })}>
      <Select.Trigger class="w-full sm:w-52" aria-label="Area" icon={area?.icon ? chosenArea : undefined}>
        <span class="truncate">{area?.label ?? "All areas"}</span>
      </Select.Trigger>
      <Select.Content>
        <Select.Item value="all" label="All areas" inset />
        {#each areas as a (a.id)}
          <Select.Item value={a.id} label={a.label}>
            {#snippet icon()}{@render areaIcon(a)}{/snippet}
          </Select.Item>
        {/each}
      </Select.Content>
    </Select.Root>
    {#if area}
      <Select.Root type="single" value={action || "any"} onValueChange={(v) => setFilter({ action: v === "any" ? "" : v })}>
        <Select.Trigger class="w-full sm:w-64" aria-label="What happened">
          <span class={["truncate", !action && "text-muted-foreground"]}>{action ? actionText(action) : "Choose what happened"}</span>
        </Select.Trigger>
        <Select.Content>
          {#each area.actions as a (a.id)}
            <Select.Item value={a.id} label={a.label} />
          {/each}
        </Select.Content>
      </Select.Root>
    {/if}
    <RepoPicker value={repo} format={formatOf(repo) || undefined} onchange={(r) => setFilter({ repo: r })} />
    <PersonPicker search={(q) => api.auditPeople(q)} value={actor} onchange={(a) => setFilter({ actor: a })} />
    <Select.Root type="single" value={when || "any"} onValueChange={pickWhen}>
      <Select.Trigger class="w-full sm:w-44" aria-label="When">
        <span class="truncate">{when ? (ranges.find((r) => r.value === when)?.label ?? "") : "Any time"}</span>
      </Select.Trigger>
      <Select.Content>
        <Select.Item value="any" label="Any time" />
        {#each ranges as r (r.value)}
          <Select.Item value={r.value} label={r.label} />
        {/each}
      </Select.Content>
    </Select.Root>
    {#if when === "custom"}
      <div class="flex items-center gap-2">
        <Input type="date" class="w-full sm:w-40" aria-label="From" value={from} max={to || undefined}
          onchange={(e) => setFilter({ from: e.currentTarget.value })} />
        <span class="text-meta" aria-hidden="true">to</span>
        <Input type="date" class="w-full sm:w-40" aria-label="To" value={to} min={from || undefined}
          onchange={(e) => setFilter({ to: e.currentTarget.value })} />
      </div>
    {/if}
  </Toolbar>

  {#if filtered}
    <div class="-mt-1 mb-(--toolbar-gap) flex flex-wrap items-center gap-2" aria-label="Filters">
      {#if action}
        <FilterChip kind="What" label={actionText(action)} onremove={() => setFilter({ action: "" })}>
          {#snippet icon()}{@render areaIcon(area)}{/snippet}
        </FilterChip>
      {/if}
      {#if repo}
        <FilterChip kind="Repository" label={repo} icon={formatOf(repo) ? repoIcon : undefined} onremove={() => setFilter({ repo: "" })} />
      {/if}
      {#if actor}
        <FilterChip kind="Who" label={actor} icon={actorIcon} onremove={() => setFilter({ actor: "" })} />
      {/if}
      {#if when}
        <FilterChip kind="When" label={rangeLabel(when, from, to)} icon={calendarIcon} onremove={() => setFilter({ when: "" })} />
      {/if}
      <Button variant="ghost" size="sm" onclick={clear}>Clear filters</Button>
    </div>
  {/if}

  {#if area && !action}
    <!-- The log filters by one action, so an area alone asks which one. -->
    <div class="flex flex-col gap-3">
      <p class="text-label">What happened in {area.label}?</p>
      <div class="flex flex-wrap gap-2">
        {#each area.actions as a (a.id)}
          <Button variant="outline" size="sm" onclick={() => setFilter({ action: a.id })}>{a.label}</Button>
        {/each}
      </div>
    </div>
  {:else if events.loading && events.items.length === 0}
    <Loading rows={4} />
  {:else if events.error && events.items.length === 0}
    <SourceError source={events} />
  {:else if events.items.length === 0}
    {#if filtered}
      <CompactEmpty icon={FunnelXIcon} text="No event matches. Change or clear the filters to see more." />
    {:else}
      <CompactEmpty icon={HistoryIcon} text="None yet." />
    {/if}
  {:else}
    {#each days as day (day.key)}
      <section class="mt-8 first:mt-0" aria-label={day.label}>
        <h3 class="mb-3 text-xs font-medium text-muted-foreground">{day.label}</h3>
        <ItemList source={day.source} caption="Events of {day.label}" key={(e) => e.id}>
          {#snippet leading(e)}
            <!-- A person has an avatar; the server itself (core, cli, a module) and a CI job show the mark. -->
            {#if e.actor_user_id != null}<UserAvatar name={e.actor} size="md" />{:else}<span class="flex size-8 shrink-0 items-center"><Logo size={20} /></span>{/if}
          {/snippet}
          {#snippet title(e)}
            {actionText(e.action)}
          {/snippet}
          {#snippet badges(e)}
            {#if refusals.has(e.action)}<StatusBadge tone="danger">Refused</StatusBadge>{/if}
          {/snippet}
          {#snippet meta(e)}
            {@const facts = detailFacts(e)}
            <span class="text-foreground">{e.actor}</span>{#if e.repo}<span aria-hidden="true">{" · "}</span><span class="inline-flex items-baseline gap-1"
                >{#if formatOf(e.repo)}<FormatIcon format={formatOf(e.repo)} class="size-3.5 self-center" />{/if}{e.repo}</span
              >{/if}{#if facts.length}<span aria-hidden="true">{" · "}</span>{facts.join(" · ")}{/if}
          {/snippet}
          {#snippet trailing(e)}
            <div class="flex flex-col items-end text-right">
              <span class="text-meta tabular-nums"><RelativeTime at={e.at} /></span>
              {#if e.ip}<span class="text-xs text-muted-foreground tabular-nums max-sm:hidden" title="Address">{e.ip}</span>{/if}
            </div>
          {/snippet}
        </ItemList>
      </section>
    {/each}
    <SourceMore source={events} />
  {/if}
{:else}
  <DataTable
    source={sessions}
    caption="Publish tokens"
    key={(s) => s.id}
    columns={[
      { label: "Who", grow: true },
      { label: "Repository" },
      { label: "State" },
      { label: "Started", align: "right" },
    ]}
  >
    {#snippet row(s)}
      <Cell grow strong text={who(s)} />
      <Cell text={s.repo} />
      <Cell>
        {#if s.state === "open"}<StatusBadge tone="success">Open</StatusBadge>{:else}<StatusBadge>{s.state.charAt(0).toUpperCase() + s.state.slice(1)}</StatusBadge>{/if}
      </Cell>
      <Cell num muted><RelativeTime at={s.created_at} /></Cell>
    {/snippet}
    {#snippet empty()}
      <CompactEmpty icon={UploadIcon} text="None yet." />
    {/snippet}
  </DataTable>
{/if}
</Section>
