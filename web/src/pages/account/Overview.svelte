<!--
  Your account: Overview. First the one identity card: your avatar, your name and your role, as plain text.
  Then, in two columns (one under 1024px), the repositories you can use and what you can do in
  each, and your devices over your recent publishes: each a Section with a list of at most five
  rows and "See all" to its full page. What you can do shows only when the rows differ. No other
  element repeats the role.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { repoPhrase, serverPhrase } from "$lib/permissions";
  import { api, type Device, type SessionInfo, type UserAccess, type UserRepoAccess } from "$lib/api";
  import { Resource, listOf } from "$lib/load.svelte";


  import { Button } from "$lib/components/ui/button";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import LaptopIcon from "@lucide/svelte/icons/laptop";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import { deviceState, noDevices, tokenSource, tokenState } from "./words";

  let {}: PageProps = $props();

  const me = $derived(auth.user);

  // The repositories you can use: the first 200, of which the list shows five.
  let access = $state<UserAccess | null>(null);
  const repos = new Resource<UserRepoAccess[]>(async () => {
    access = await api.myAccess({ limit: 200 });
    return access.items;
  });
  const SHOWN = 5;

  const canPublish = $derived(access?.can_publish ?? !me?.disabled);
  const publishes = (r: UserRepoAccess) => !!r.perms?.includes("repo.publish") && canPublish;
  const publishCount = $derived((repos.data ?? []).filter(publishes).length);

  // Your own devices (the list of a holder of People holds everyone's) and your recent publish tokens.
  const devices = new Resource<Device[]>(async () => {
    const page = await api.devices.list({ limit: 200 });
    return page.items.filter((d) => !d.username || d.username === me?.username);
  });
  const recent = new Resource<SessionInfo[]>(async () => (await api.publish.sessions({ limit: 5 })).items);

  // A public repository without a grant: download only.
  const youCan = (r: UserRepoAccess) => repoPhrase(r.perms?.length ? r.perms : ["repo.download"]);
  // Say it once: what you can do shows only when the rows differ.
  const mixed = $derived(new Set((repos.data ?? []).map(youCan)).size > 1);
  const allRepos = $derived(auth.can("repositories.all"));
  const through: Record<UserRepoAccess["via"], string> = {
    all_repos: "All repositories",
    permission: "Access given to you",
    public: "Public repository",
  };
  const openRepo = (r: UserRepoAccess) => href(repoPath(r.repo));

</script>

<PageHeader title="Overview" />

{#if me}
  <Section>
    <div class="flex min-w-0 items-center gap-4">
      <UserAvatar name={me.username} size="lg" />
      <div class="min-w-0">
        <p class="truncate text-section">{me.username}</p>
        <p class="mt-1 truncate text-meta text-muted-foreground">{serverPhrase(me.permissions) || "Member"}</p>
      </div>
    </div>
  </Section>
{/if}

<div class="mt-(--section-gap) grid items-start gap-(--section-gap) lg:grid-cols-2">
  <Section title="Your repositories">
    {#snippet actions()}
      {#if (repos.data?.length ?? 0) > SHOWN}<Button variant="outline" size="sm" href={href("/browse")} aria-label="See all repositories">See all</Button>{/if}
    {/snippet}
    <!-- A holder of All repositories reaches every repository the same way: the identity card says it once. -->
    <ItemList
      source={listOf(repos, (r) => r.slice(0, SHOWN))}
      caption="Your repositories"
      key={(r) => r.repo}
      href={openRepo}
      meta={mixed || !allRepos ? repoMeta : undefined}
    >
      {#snippet leading(r)}<FormatIcon format={r.format} class="size-5" />{/snippet}
      {#snippet title(r)}{r.repo}{/snippet}
      {#snippet empty()}
        <CompactEmpty icon={DatabaseIcon} text="No repository is public, and nobody gave you access yet. Ask an administrator." />
      {/snippet}
    </ItemList>
  </Section>

  <div>
    <Section title="Your devices" description="Computers that publish as you with the Telesphorepo CLI.">
      {#snippet actions()}
        {#if devices.data?.length}<Button variant="outline" size="sm" href={href("/account/devices")} aria-label="See all devices">See all</Button>{/if}
      {/snippet}
      <ItemList source={listOf(devices, (d) => d.slice(0, SHOWN))} caption="Your devices" key={(d) => d.id} href={() => href("/account/devices")}>
        {#snippet leading()}<LaptopIcon class="size-5" />{/snippet}
        {#snippet title(d)}{d.name}{/snippet}
        {#snippet meta(d)}{#if d.last_used_at}Used <RelativeTime at={d.last_used_at} />{:else}Never used{/if}{/snippet}
        {#snippet trailing(d)}
          {@const state = deviceState(d)}
          <StatusBadge tone={state.tone}>{state.word}</StatusBadge>
        {/snippet}
        {#snippet empty()}
          <CompactEmpty icon={LaptopIcon} text={noDevices}>
            <Button variant="outline" size="sm" href={href("/device")}>Approve a device</Button>
          </CompactEmpty>
        {/snippet}
      </ItemList>
    </Section>

    <Section title="Recent publishes" description="The publish tokens that you and the Telesphorepo CLI used last.">
      {#snippet actions()}
        {#if recent.data?.length}<Button variant="outline" size="sm" href={href("/account/tokens")} aria-label="See all publish tokens">See all</Button>{/if}
      {/snippet}
      <ItemList source={listOf(recent)} caption="Recent publishes" key={(s) => s.id} href={(s) => href(repoPath(s.repo))}>
        {#snippet title(s)}{s.repo}{/snippet}
        {#snippet meta(s)}{tokenSource(s)} · started <RelativeTime at={s.created_at} />{/snippet}
        {#snippet trailing(s)}
          {@const st = tokenState(s)}
          <StatusBadge tone={st.tone}>{st.word}</StatusBadge>
        {/snippet}
        {#snippet empty()}
          <CompactEmpty
            icon={UploadIcon}
            text={canPublish && publishCount
              ? "None yet."
              : "You cannot publish to a repository yet. Ask a person who manages its access for the Publish permission."}
          />
        {/snippet}
      </ItemList>
    </Section>
  </div>
</div>

{#snippet repoMeta(r: UserRepoAccess)}{[mixed ? youCan(r) : "", allRepos ? "" : through[r.via]].filter(Boolean).join(" · ")}{/snippet}