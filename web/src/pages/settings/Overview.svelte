<!--
  All settings, the administrator's home: about this server. The header names the server, its
  version and its public address (for a holder of the General settings); under it the actions
  people take most as doors (New repository, New account, New CI pipeline; each only for a person
  who may). Then one row of equal stat tiles: the repositories (GET /api/repos) by kind, their
  files and the stored size, and the people, the CI pipelines and the signing keys, each only for a
  person who can open its page, each a link to it. A holder of All repositories counts every
  repository; anyone else their repositories, the ones where they hold more than download, under
  the heading "Your repositories". Then where the space goes: the eight largest repositories as
  bars, each with its format's icon. Then, for a holder
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
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Stat from "./overview/Stat.svelte";
  import SizeBars from "./overview/SizeBars.svelte";
  import DownloadBars from "./overview/DownloadBars.svelte";
  import DownloadIcon from "@lucide/svelte/icons/download";
  import NewAccountDialog from "../server/people/NewAccountDialog.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import HistoryIcon from "@lucide/svelte/icons/history";
  import PackagePlusIcon from "@lucide/svelte/icons/package-plus";
  import UserPlusIcon from "@lucide/svelte/icons/user-plus";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import DoorCard from "$lib/components/DoorCard.svelte";
  import DoorRow from "$lib/components/DoorRow.svelte";
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

  // The kinds, only when there is more than one: "10 hosted" under "10" says nothing new.
  const kinds = $derived.by(() => {
    const n = { hosted: 0, proxy: 0, group: 0 };
    for (const r of mine) if (r.kind in n) n[r.kind as keyof typeof n]++;
    const parts = [
      n.hosted ? `${formatCount(n.hosted)} hosted` : "",
      n.proxy ? count(n.proxy, "proxy", "proxies") : "",
      n.group ? count(n.group, "group", "groups") : "",
    ].filter(Boolean);
    return parts.length > 1 ? parts.join(" · ") : "";
  });
  // A group stores nothing of its own: its members hold the files it serves.
  const stores = $derived(mine.filter((r) => r.kind !== "group"));
  const files = $derived(stores.reduce((n, r) => n + r.file_count, 0));
  const stored = $derived(stores.reduce((n, r) => n + r.total_size, 0));
  const cached = $derived(mine.filter((r) => r.kind === "proxy").reduce((n, r) => n + r.total_size, 0));
  const largest = $derived([...stores].filter((r) => r.total_size > 0).sort((a, b) => b.total_size - a.total_size).slice(0, 8));
  const share = (r: Repo) => (stored ? Math.round((r.total_size / stored) * 100) : 0);
  const repoHref = (r: Repo) => href(auth.managesRepo(r.name) ? repoSettingsPath(r.name) : repoPath(r.name));

  // --- people, CI pipelines, signing keys: each only for a person who can open its page ---
  const people = opens("/settings/people") ? new Resource(() => every((p) => api.users.list(p))) : null;
  const pipelines = opens("/settings/ci-pipelines") ? new Resource(() => every((p) => api.publish.trusts.list(p))) : null;
  const keys = opens("/settings/signing-keys") ? new Resource(() => api.signingKeys.list()) : null;
  const disabledPeople = $derived((people?.data ?? []).filter((u) => u.disabled).length);
  const offPipelines = $derived((pipelines?.data ?? []).filter((t) => t.disabled).length);
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
  </PageHeader>

  {#if canNewRepo || canNewAccount || canNewPipeline}
    <DoorRow label="Create">
      {#if canNewRepo}
        <DoorCard title="New repository" text="Hosted, a proxy of a remote repository, or a group of several." href={href("/settings/repositories/new")} icon={PackagePlusIcon} />
      {/if}
      {#if canNewAccount}
        <DoorCard title="New account" text="A name and a first password; permissions come after." onclick={() => (newAccount = true)} icon={UserPlusIcon} />
      {/if}
      {#if canNewPipeline}
        <DoorCard title="New CI pipeline" text="A GitHub Actions or Jenkins job publishes with no stored secret." href={href("/settings/ci-pipelines/new")} icon={WorkflowIcon} />
      {/if}
    </DoorRow>
  {/if}

  <!-- The figures: one row of equal tiles. Without All repositories the repository figures are the person's own. -->
  <Section title={allRepos ? undefined : "Your repositories"} plain>
    {#if repos.error && !repos.data}
      <ErrorState error={repos.error} onRetry={repos.reload} />
    {:else if repos.data && mine.length === 0}
      <CompactEmpty icon={DatabaseIcon} text={canNewRepo ? "No repository yet." : "You manage no repository yet."} />
    {:else}
      <div class="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        <Stat label="Repositories" detail={kinds || undefined} href={opens("/settings/repositories") ? href("/settings/repositories") : undefined}>
          {@render repoFigure(formatCount(mine.length))}
        </Stat>
        <Stat label="Files">{@render repoFigure(formatCount(files))}</Stat>
        <Stat label="Stored" detail={cached ? `${formatSize(cached)} cached by proxies` : undefined}>{@render repoFigure(formatSize(stored))}</Stat>
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
          <Stat label="Signing keys" href={href("/settings/signing-keys")}>
            {@render figure(keys)}
          </Stat>
        {/if}
      </div>
    {/if}
  </Section>

  {#if largest.length}
    <!-- Where the space goes: one chart, a bar per repository with its format's icon. -->
    <Section title="Largest repositories">
      <SizeBars
        label="Largest repositories"
        rows={largest.map((r) => ({
          key: r.name,
          format: r.format,
          name: r.name,
          size: r.total_size,
          href: repoHref(r),
          note: `${r.name}: ${formatSize(r.total_size)}, ${share(r)}% of the stored size, ${count(r.file_count, "file", "files")}${r.last_published_at ? `, last published ${formatDateTime(r.last_published_at)}` : ""}`,
        }))}
      />
    </Section>
  {/if}

  {#if downloads}
    <!-- With no downloads the empty row names the span itself. -->
    <Section title="Downloads" description={downloads.data?.downloads === 0 ? undefined : `The last ${statDays} days, by day in UTC.`}>
      {#if downloads.loading && !downloads.data}
        <Loading rows={3} />
      {:else if downloads.error && !downloads.data}
        <ErrorState error={downloads.error} onRetry={downloads.reload} />
      {:else if downloads.data && downloads.data.downloads === 0}
        <CompactEmpty icon={DownloadIcon} text="No downloads in the last {statDays} days." />
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
          <CompactEmpty icon={HistoryIcon} text="Nothing has happened yet." />
        {/snippet}
      </ItemList>
    </Section>
  {/if}

  {#if canNewAccount}<NewAccountDialog bind:open={newAccount} />{/if}
{/if}

<!-- A figure of the repositories: Loading until GET /api/repos answers. -->
{#snippet repoFigure(value: string)}
  {#if repos.loading && !repos.data}<Loading inline label="Loading" />{:else}{value}{/if}
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
