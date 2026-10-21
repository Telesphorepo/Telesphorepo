<!--
  The server permissions as a role. Three radio cards lead: Member (no server permissions),
  Administrator (every server permission) and Custom. Picking Member or Administrator sets the
  value at once; Custom keeps it and opens the list: one row per server permission with its
  label, its one line of help and a switch at the right. The rows sit under the groups of the
  Settings sidebar, where the page that the permission opens sits (the first page whose access is
  the permission, with that page's icon); a permission without a page sits under Other. A
  permission that gives all repositories says what that holds. A permission that a switched-on
  one needs is on and locked ("Needed by Manage people"). A permission outside `held` cannot
  change, because the server lets a person give and remove only the permissions they hold; so
  Administrator needs every missing one held, and Member every present one.

  `readonly` shows the role and, for Custom, the permissions held, without controls. The cards
  stack in a narrow container (a dialog, a phone) and sit side by side from 36rem.

    <ServerPermissionPicker id="p" held={mine} bind:value={perms} />
-->
<script lang="ts">
  import { untrack, type Component } from "svelte";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import { Switch } from "$lib/components/ui/switch";
  import type { PermId, PermissionDef } from "$lib/api";
  import { listPhrase, repoPermissions, serverPermissionDefs, serverRole, type ServerRole } from "$lib/permissions";
  import { navPlace, navSections, pages } from "../../modules";
  import { cn } from "$lib/utils";
  import LockIcon from "@lucide/svelte/icons/lock";

  let {
    id,
    value = $bindable([]),
    held,
    readonly = false,
    onchange,
  }: {
    /** Prefix of the element ids, unique on the page. */
    id: string;
    value?: PermId[];
    /** The permissions the person can change; undefined: every one. */
    held?: readonly PermId[];
    /** Show the role and the permissions without controls. */
    readonly?: boolean;
    onchange?: (value: PermId[]) => void;
  } = $props();

  type Item = { def: PermissionDef; icon?: Component<{ class?: string }> };
  type Area = { title: string; items: Item[] };

  const defs = serverPermissionDefs();
  const all = defs.map((d) => d.id);
  const labelOf = (p: PermId) => defs.find((d) => d.id === p)?.label ?? p;
  const canChange = (p: PermId) => !held || held.includes(p);

  // The groups of the Settings sidebar in their order, each permission at the place of its page.
  const areas: Area[] = (() => {
    const placed = defs.map((def, i) => {
      const page = pages.find((p) => p.access === def.id && p.layout === "settings");
      const place = page ? navPlace(page.path) : null;
      return { def, i, place };
    });
    const out: Area[] = navSections.map((g) => ({
      title: g.label,
      items: placed
        .filter((x) => x.place?.group.id === g.id)
        .sort((a, b) => a.place!.item.nav!.order - b.place!.item.nav!.order || a.i - b.i)
        .map((x) => ({ def: x.def, icon: x.place!.item.nav?.icon })),
    }));
    out.push({ title: "Other", items: placed.filter((x) => !x.place).map((x) => ({ def: x.def })) });
    return out.filter((a) => a.items.length > 0);
  })();

  const reposLine = `On every repository, new ones too: ${listPhrase(repoPermissions().map((p) => p.label))}.`;

  // Custom was picked while the value holds none or every permission. A value from outside (the
  // save bar's Discard) shows its own role again.
  let custom = $state(false);
  let emitted = "";
  const key = (v: readonly PermId[]) => [...v].sort().join(",");
  $effect(() => {
    const k = key(value);
    if (k !== emitted) untrack(() => ((emitted = k), (custom = false)));
  });
  const role = $derived<ServerRole>(custom ? "custom" : serverRole(value));

  const canAdmin = $derived(all.every((p) => value.includes(p) || canChange(p)));
  const canMember = $derived(value.every(canChange));

  const roles = $derived([
    {
      value: "member" as const,
      label: "Member",
      text: canMember ? "No server permissions. Uses only the repositories they are given." : "You cannot remove a permission that you do not hold.",
      disabled: !canMember,
    },
    {
      value: "administrator" as const,
      label: "Administrator",
      text: canAdmin ? "Every server permission: manages the whole server." : "You do not hold every server permission, so you cannot give them all.",
      disabled: !canAdmin,
    },
    { value: "custom" as const, label: "Custom", text: "Manages only the parts of the server that you pick.", disabled: false },
  ]);

  function set(next: Set<PermId>) {
    value = all.filter((p) => next.has(p));
    emitted = key(value);
    onchange?.(value);
  }

  function choose(r: ServerRole) {
    if (r === "custom") {
      custom = true;
      return;
    }
    custom = false;
    set(new Set(r === "administrator" ? all : []));
  }

  const needs = Object.fromEntries(defs.map((d) => [d.id, d.needs])) as Record<PermId, PermId[]>;

  /** Adds p and what it needs, or removes p and what needs it. */
  function apply(out: Set<PermId>, p: PermId, on: boolean) {
    if (on) {
      if (out.has(p)) return;
      out.add(p);
      for (const n of needs[p] ?? []) apply(out, n, true);
      return;
    }
    if (!out.delete(p)) return;
    for (const k of all) if (needs[k]?.includes(p)) apply(out, k, false);
  }

  function toggle(p: PermId, on: boolean) {
    const out = new Set(value);
    apply(out, p, on);
    set(out);
  }

  /** The labels of the switched-on permissions that need p. */
  const neededBy = (p: PermId) => all.filter((k) => value.includes(k) && needs[k]?.includes(p)).map(labelOf);

  const shown = $derived(readonly ? areas.map((a) => ({ ...a, items: a.items.filter((i) => value.includes(i.def.id)) })).filter((a) => a.items.length) : areas);
  const current = $derived(roles.find((r) => r.value === role)!);
</script>

{#snippet row(item: Item)}
  {@const d = item.def}
  {@const on = value.includes(d.id)}
  {@const by = neededBy(d.id)}
  {@const own = canChange(d.id)}
  <li class="flex items-start gap-3 px-4 py-3">
    {#if item.icon}<item.icon class="mt-0.5 size-4 shrink-0 text-muted-foreground" />{/if}
    <svelte:element this={readonly ? "div" : "label"} for={readonly ? undefined : `${id}-${d.id}`} class={cn("flex min-w-0 flex-1 flex-col gap-1", !readonly && own && !(on && by.length) && "cursor-pointer")}>
      <span id="{id}-{d.id}-label" class="text-label">{d.label}</span>
      <span id="{id}-{d.id}-help" class="text-meta text-muted-foreground">
        {d.help}{#if d.all_repos}{" "}{reposLine}{/if}
      </span>
      {#if !readonly && on && by.length}
        <span class="flex items-center gap-1.5 text-meta text-muted-foreground"><LockIcon class="size-3.5 shrink-0" aria-hidden="true" />Needed by {by.join(" and ")}</span>
      {:else if !readonly && !own}
        <span class="flex items-center gap-1.5 text-meta text-muted-foreground"
          ><LockIcon class="size-3.5 shrink-0" aria-hidden="true" />You do not hold it, so you cannot change it.</span
        >
      {/if}
    </svelte:element>
    {#if !readonly}
      <Switch
        id="{id}-{d.id}"
        class="mt-0.5"
        checked={on}
        disabled={!own || (on && by.length > 0)}
        aria-labelledby="{id}-{d.id}-label"
        aria-describedby="{id}-{d.id}-help"
        onCheckedChange={(v) => toggle(d.id, v)}
      />
    {/if}
  </li>
{/snippet}

<div class="@container flex flex-col gap-6">
  {#if readonly}
    <div class="flex flex-col gap-1">
      <p class="text-label">{current.label}</p>
      <p class="text-meta text-muted-foreground">{role === "custom" ? `${value.length} of ${all.length} server permissions.` : current.text}</p>
    </div>
  {:else}
    <RadioGroup.Root class="grid gap-2 @xl:grid-cols-3" aria-label="Role" value={role} onValueChange={(v) => choose(v as ServerRole)}>
      {#each roles as r (r.value)}
        <label
          for="{id}-role-{r.value}"
          class={cn(
            "flex items-start gap-3 rounded-lg border p-4 transition-colors",
            r.value === role ? "border-transparent bg-selected text-selected-foreground" : r.disabled ? "cursor-not-allowed" : "cursor-pointer hover:bg-hover",
          )}
        >
          <RadioGroup.Item id="{id}-role-{r.value}" value={r.value} disabled={r.disabled} class="mt-0.5" aria-describedby="{id}-role-{r.value}-text" />
          <span class="flex min-w-0 flex-col gap-1">
            <span class="text-label">{r.label}</span>
            <span id="{id}-role-{r.value}-text" class="text-meta text-muted-foreground">{r.text}</span>
          </span>
        </label>
      {/each}
    </RadioGroup.Root>
  {/if}

  {#if role === "custom"}
    {#each shown as a, ai (a.title)}
      <div class="flex flex-col gap-2" role="group" aria-labelledby="{id}-a{ai}">
        <h3 id="{id}-a{ai}" class="text-xs font-medium text-muted-foreground">{a.title}</h3>
        <ul class="flex flex-col divide-y rounded-lg border">
          {#each a.items as item (item.def.id)}{@render row(item)}{/each}
        </ul>
      </div>
    {/each}
  {/if}
</div>
