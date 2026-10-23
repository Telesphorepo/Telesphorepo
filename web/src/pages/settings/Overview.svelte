<!--
  All settings, the administrator's home: about this server. The header names the server, its
  version and its public address (for a holder of the General settings), with the actions people
  take most (New repository, New account, New CI pipeline; each only for a person who may). Then
  what the repositories hold (GET /api/repos): how many by kind, the files, the stored size and the
  last publish; the size by format as bars, and the five largest repositories. A holder of All
  repositories sees every repository; anyone else sees their repositories, the ones where they
  hold more than download, and the page says so. Then the people, the CI pipelines and the
  signing keys, each only for a person who can open its page, each a link to it; and for a holder
  of Activity the downloads of the last 30 days (GET /api/stats: the total, the bytes served, a
  column per day, the five most downloaded repositories) and the last five events. The sidebar lists the pages: this page does not repeat it.
  A person who opens no Settings page goes on to Your account, in place of this address.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { current } from "$lib/current.svelte";
  import { brand } from "$lib/brand.svelte";
  import { api, type Page, type PageParams, type Repo } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";
  import { formatCount, formatDateTime, formatSize } from "$lib/format";
  import { count } from "$lib/words";
  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import RepoKindIcon from "$lib/components/RepoKindIcon.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import { Separator } from "$lib/components/ui/separator";
  import Stat from "./overview/Stat.svelte";
  import SizeBars from "./overview/SizeBars.svelte";
  import DownloadBars from "./overview/DownloadBars.svelte";
  import DownloadIcon from "@lucide/svelte/icons/download";
  import NewAccountDialog from "../server/people/NewAccountDialog.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import HistoryIcon from "@lucide/svelte/icons/history";
  import { adminNav, canOpen, pages } from "../../modules";

  let {}: PageProps = $props();

  const hasAdmin = $derived(adminNav().length > 0);
  $effect(() => {
    if (!hasAdmin) navigate("/account", { replace: true });
  });

  /** True when the person may open the page at path (its access in the module registry). */
  const opens = (path: string) => {
    const p = pages.find((x) => x.path === path);
    return !!p && canOpen(p);
  };

  /** Every item of a paged list, 200 a page. */
  async function every<T>(fn: (p: PageParams) => Promise<Page<T>>): Promise<T[]> {
    const out: T[] = [];
    let after: string | undefined;
    do {
      const p = await fn({ after, limit: 200 });
      out.push(...p.items);
      after = p.next ?? undefined;
    } while (after);
    return out;
  }

  // --- the server ---
  const version = new Resource(async () => (await api.changes()).version);
  const settings = auth.can("settings.general") ? new Resource(() => api.settings.get()) : null;
  const baseURL = $derived(
    String(settings?.data?.groups.flatMap((g) => g.settings).find((s) => s.key === "base_url")?.value ?? ""),
  );

  // --- the repositories ---
  const repos = new Resource(() => api.repos.list());
  const allRepos = $derived(auth.allRepos());
  // Without All repositories: the repositories where the person holds more than download.
  const mine = $derived((repos.data ?? []).filter((r) => allRepos || r.permissions.some((p) => p !== "repo.download")));

  const kinds = $derived.by(() => {
    const n = { hosted: 0, proxy: 0, group: 0 };
    for (const r of mine) if (r.kind in n) n[r.kind as keyof typeof n]++;
    return [
      n.hosted ? `${formatCount(n.hosted)} hosted` : "",
      n.proxy ? count(n.proxy, "proxy", "proxies") : "",
      n.group ? count(n.group, "group", "groups") : "",
    ]
      .filter(Boolean)
      .join(" · ");
  });
  const files = $derived(mine.reduce((n, r) => n + r.file_count, 0));
  const stored = $derived(mine.reduce((n, r) => n + r.total_size, 0));
  const cached = $derived(mine.filter((r) => r.kind === "proxy").reduce((n, r) => n + r.total_size, 0));
  const last = $derived(
    mine.reduce<Repo | null>(
      (best, r) => (r.last_published_at && (!best || r.last_published_at > best.last_published_at!) ? r : best),
      null,
    ),
  );

  // A group stores nothing of its own: its members hold its files.
  const stores = $derived(mine.filter((r) => r.kind !== "group"));
  const byFormat = $derived.by(() => {
    const m = new Map<string, { format: string; size: number; repos: number }>();
    for (const r of stores) {
      const row = m.get(r.format) ?? { format: r.format, size: 0, repos: 0 };
      row.size += r.total_size;
      row.repos++;
      m.set(r.format, row);
    }
    return [...m.values()].sort((a, b) => b.size - a.size || a.format.localeCompare(b.format));
  });
  const largest = $derived([...stores].filter((r) => r.total_size > 0).sort((a, b) => b.total_size - a.total_size).slice(0, 5));
  const repoHref = (r: Repo) => href(auth.managesRepo(r.name) ? repoSettingsPath(r.name) : repoPath(r.name));

  // --- people, CI pipelines, signing keys: each only for a person who can open its page ---
  const people = opens("/settings/people") ? new Resource(() => every((p) => api.users.list(p))) : null;
  const pipelines = opens("/settings/ci-pipelines") ? new Resource(() => every((p) => api.publish.trusts.list(p))) : null;
  const keys = opens("/settings/signing-keys") ? new Resource(() => api.signingKeys.list()) : null;
  const disabledPeople = $derived((people?.data ?? []).filter((u) => u.disabled).length);
  const offPipelines = $derived((pipelines?.data ?? []).filter((t) => t.disabled).length);
  const signedRepos = $derived(new Set((keys?.data ?? []).flatMap((k) => k.repos)).size);
  const others = !!(people || pipelines || keys);

  // --- recent activity ---
  const activity = opens("/settings/activity");
  const events = activity ? new Resource(async () => (await api.audit({ limit: 5 })).items) : null;
  const actions = activity ? new Resource(() => api.auditActions()) : null;
  const actionText = (a: string) => actions?.data?.find((x) => x.id === a)?.label ?? a;
  const refusals = new Set(["signin.failed", "sso.signin.failed", "network.denied"]);

  // --- downloads of the last 30 days, for a holder of Activity ---
  const statDays = 30;
  const downloads = activity ? new Resource(() => api.stats(statDays)) : null;
  const topDownloads = $derived((downloads?.data?.repos ?? []).filter((r) => r.downloads > 0).slice(0, 5));
  const repoOf = (name: string) => repos.data?.find((r) => r.name === name);

  // --- quick actions ---
  const canNewRepo = opens("/settings/repositories/new");
  const canNewAccount = opens("/settings/people");
  const canNewPipeline = opens("/settings/ci-pipelines/new");
  let newAccount = $state(false);
</script>

{#if hasAdmin}
  <PageHeader title={brand.name} description={baseURL || undefined} crumbs={[{ label: current.route?.title ?? "All settings" }]}>
    {#snippet meta()}
      {#if version.data}<StatusBadge>{version.data}</StatusBadge>{/if}
    {/snippet}
    {#snippet actions()}
      {#if canNewRepo}<Button href={href("/settings/repositories/new")}><PlusIcon />New repository</Button>{/if}
      {#if canNewAccount}<Button variant="outline" onclick={() => (newAccount = true)}><PlusIcon />New account</Button>{/if}
      {#if canNewPipeline}<Button variant="outline" href={href("/settings/ci-pipelines/new")}><PlusIcon />New CI pipeline</Button>{/if}
    {/snippet}
  </PageHeader>

  <!-- The figures: one card for a holder of All repositories; else "Your repositories" heads its own card. -->
  {#if allRepos}
    <Section>
      {@render repoFigures()}
      {#if others}<Separator class="my-6" />{@render otherFigures()}{/if}
    </Section>
  {:else}
    <Section title="Your repositories">{@render repoFigures()}</Section>
    {#if others}<Section>{@render otherFigures()}</Section>{/if}
  {/if}
  {#if largest.length}
    <div class="mt-(--section-gap) grid gap-(--section-gap) {byFormat.length > 1 ? 'lg:grid-cols-2' : ''}">
      {#if byFormat.length > 1}
        <Section title="Size by format" class="mt-0">
          <SizeBars rows={byFormat} />
        </Section>
      {/if}
      <Section title="Largest repositories" class="mt-0">
        <ItemList source={listOf(repos, () => largest)} caption="Largest repositories" key={(r) => r.name} href={repoHref}>
          {#snippet leading(r)}<FormatIcon format={r.format} class="size-5" />{/snippet}
          {#snippet title(r)}{r.name}{/snippet}
          {#snippet badges(r)}<RepoKindIcon kind={r.kind} />{/snippet}
          {#snippet meta(r)}{count(r.file_count, "file", "files")}{#if r.last_published_at}{" · published "}<RelativeTime at={r.last_published_at} />{/if}{/snippet}
          {#snippet trailing(r)}<span class="text-body tabular-nums">{formatSize(r.total_size)}</span>{/snippet}
        </ItemList>
      </Section>
    </div>
  {/if}

  {#if downloads}
    <Section title="Downloads" description="The last {statDays} days, by day in UTC.">
      {#if downloads.loading && !downloads.data}
        <Loading rows={3} />
      {:else if downloads.error && !downloads.data}
        <ErrorState error={downloads.error} onRetry={downloads.reload} />
      {:else if downloads.data && downloads.data.downloads === 0}
        <CompactEmpty icon={DownloadIcon} text="No downloads in the last {statDays} days. Each file that a client gets counts here." />
      {:else if downloads.data}
        {@const st = downloads.data}
        <div class="grid gap-8 lg:grid-cols-2">
          <div class="flex min-w-0 flex-col gap-6">
            <div class="grid grid-cols-2 gap-6">
              <Stat label="Downloads" value={formatCount(st.downloads)} />
              <Stat label="Served" value={formatSize(st.bytes)} />
            </div>
            <DownloadBars days={st.days} />
          </div>
          <div class="min-w-0">
            <p class="mb-3 text-xs font-medium text-muted-foreground">Most downloaded</p>
            <ItemList
              source={{ items: topDownloads, loading: false, error: "" }}
              caption="Most downloaded repositories"
              key={(r) => r.name}
              href={(r) => {
                const repo = repoOf(r.name);
                return repo ? repoHref(repo) : href(repoPath(r.name));
              }}
            >
              {#snippet leading(r)}<FormatIcon format={r.format} class="size-5" />{/snippet}
              {#snippet title(r)}{r.name}{/snippet}
              {#snippet meta(r)}{formatSize(r.bytes)} served{/snippet}
              {#snippet trailing(r)}<span class="text-body tabular-nums">{formatCount(r.downloads)}</span>{/snippet}
            </ItemList>
          </div>
        </div>
      {/if}
    </Section>
  {/if}

  {#if events}
    <Section title="Recent activity">
      {#snippet actions()}
        {#if events.data?.length}<Button variant="outline" size="sm" href={href("/settings/activity")} aria-label="See all activity">See all</Button>{/if}
      {/snippet}
      <ItemList source={listOf(events)} caption="Recent activity" key={(e) => e.id} href={() => href("/settings/activity")}>
        {#snippet title(e)}{actionText(e.action)}{#if e.target && e.target !== e.actor}<span class="text-muted-foreground">{" · "}{e.target}</span>{/if}{/snippet}
        {#snippet badges(e)}{#if refusals.has(e.action)}<StatusBadge tone="danger">Refused</StatusBadge>{/if}{/snippet}
        {#snippet meta(e)}{e.actor || "Someone"}{#if e.repo}{" · "}{e.repo}{/if}{" · "}<span title={formatDateTime(e.at)}><RelativeTime at={e.at} /></span>{/snippet}
        {#snippet empty()}
          <CompactEmpty icon={HistoryIcon} text="None yet. Sign-ins, uploads and changes show here as people use the server." />
        {/snippet}
      </ItemList>
    </Section>
  {/if}

  {#if canNewAccount}<NewAccountDialog bind:open={newAccount} />{/if}
{/if}

{#snippet repoFigures()}
  {#if repos.loading && !repos.data}
    <Loading rows={2} />
  {:else if repos.error && !repos.data}
    <ErrorState error={repos.error} onRetry={repos.reload} />
  {:else if mine.length === 0}
    <CompactEmpty icon={DatabaseIcon} text={canNewRepo ? "No repository yet. Create one to publish to." : "You manage no repository yet."}>
      {#if canNewRepo}<Button variant="outline" href={href("/settings/repositories/new")}><PlusIcon />New repository</Button>{/if}
    </CompactEmpty>
  {:else}
    <div class="grid grid-cols-2 gap-6 lg:grid-cols-4">
      <Stat label="Repositories" value={formatCount(mine.length)} detail={kinds} href={opens("/settings/repositories") ? href("/settings/repositories") : undefined} />
      <Stat label="Files" value={formatCount(files)} />
      <Stat label="Stored" value={formatSize(stored)} detail={cached ? `${formatSize(cached)} cached by proxies` : undefined} />
      <Stat label="Last publish" detail={last ? `to ${last.name}` : undefined}>
        {#if last}<span class="truncate" title={formatDateTime(last.last_published_at!)}><RelativeTime at={last.last_published_at} /></span>{:else}Never{/if}
      </Stat>
    </div>
  {/if}
{/snippet}

{#snippet otherFigures()}
  <div class="grid grid-cols-2 gap-6 lg:grid-cols-4">
    {#if people}
      <Stat label="People" href={href("/settings/people")} detail={disabledPeople ? `${formatCount(disabledPeople)} disabled` : undefined}>
        {@render figure(people)}
      </Stat>
    {/if}
    {#if pipelines}
      <Stat label="CI pipelines" href={href("/settings/ci-pipelines")} detail={offPipelines ? `${formatCount(offPipelines)} turned off` : undefined}>
        {@render figure(pipelines)}
      </Stat>
    {/if}
    {#if keys}
      <Stat
        label="Signing keys"
        href={href("/settings/signing-keys")}
        detail={keys.data?.length ? `used by ${count(signedRepos, "repository", "repositories")}` : undefined}
      >
        {@render figure(keys)}
      </Stat>
    {/if}
  </div>
{/snippet}

<!-- A count that loads on its own: Loading, then the value, or the error with Try again. -->
{#snippet figure(r: Resource<unknown[]>)}
  {#if r.loading && r.data === undefined}
    <Loading inline label="Loading" />
  {:else if r.error && r.data === undefined}
    <ErrorState inline error={r.error} onRetry={r.reload} />
  {:else if r.data !== undefined}
    {formatCount(r.data.length)}
  {/if}
{/snippet}
