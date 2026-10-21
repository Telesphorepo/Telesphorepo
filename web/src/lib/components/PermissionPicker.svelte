<!--
  Permissions as checkboxes in groups, one group per subsystem of the permission registry: each
  group has the subsystem's label with All and None and a line that says what the subsystem is,
  and each checkbox has one line of help. `needs` holds the dependency rules: a check also checks
  what the permission needs, a clear also clears what needs it, and the help line of a permission
  that another one needs says why it is checked. A permission outside `held` cannot change,
  because the server lets a person give and remove only the permissions they hold.

    <PermissionPicker id="p" groups={pickerGroups("repo")} needs={needsOf("repo")} held={mine} bind:value={perms} />
-->
<script lang="ts" generics="P extends string">
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import OptionRow from "./OptionRow.svelte";
  import type { PermissionInfo } from "$lib/permissions";

  let {
    id,
    groups,
    value = $bindable([]),
    needs = {},
    held,
    onchange,
  }: {
    /** Prefix of the element ids, unique on the page. */
    id: string;
    groups: { title: string; help?: string; items: PermissionInfo<P>[] }[];
    value?: P[];
    /** A permission and the permissions it needs. */
    needs?: Partial<Record<P, P[]>>;
    /** The permissions the person can change; undefined: every one. */
    held?: readonly P[];
    onchange?: (value: P[]) => void;
  } = $props();

  const order = $derived(groups.flatMap((g) => g.items.map((i) => i.name)));
  const labelOf = (p: P) => groups.flatMap((g) => g.items).find((i) => i.name === p)?.label ?? p;
  const canChange = (p: P) => !held || held.includes(p);

  function set(next: Set<P>) {
    value = order.filter((p) => next.has(p));
    onchange?.(value);
  }

  /** Adds p and what it needs, or removes p and what needs it. */
  function apply(out: Set<P>, p: P, on: boolean) {
    if (on) {
      if (out.has(p)) return;
      out.add(p);
      for (const n of needs[p] ?? []) apply(out, n, true);
      return;
    }
    if (!out.delete(p)) return;
    for (const k of Object.keys(needs) as P[]) if (needs[k]?.includes(p)) apply(out, k, false);
  }

  function toggle(p: P, on: boolean) {
    const out = new Set(value);
    apply(out, p, on);
    set(out);
  }

  function all(items: PermissionInfo<P>[], on: boolean) {
    const out = new Set(value);
    for (const i of items) if (canChange(i.name)) apply(out, i.name, on);
    set(out);
  }

  /** "Publish needs it." for a checked permission that a checked one needs. */
  function reason(p: P): string {
    const by = (Object.keys(needs) as P[]).filter((k) => needs[k]?.includes(p) && value.includes(k));
    return by.length ? ` ${by.map(labelOf).join(" and ")} ${by.length === 1 ? "needs" : "need"} it.` : "";
  }
</script>

<div class="flex flex-col gap-4">
  {#each groups as g, gi (g.title)}
    {@const changeable = g.items.filter((i) => canChange(i.name))}
    <div class="flex flex-col gap-1" role="group" aria-labelledby="{id}-g{gi}">
      <div class="flex items-center gap-1">
        <div class="flex min-w-0 flex-col">
          <h3 id="{id}-g{gi}" class="text-label">{g.title}</h3>
          {#if g.help}<p class="text-meta text-muted-foreground">{g.help}</p>{/if}
        </div>
        <Button
          class="ml-auto"
          variant="ghost"
          size="xs"
          disabled={changeable.every((i) => value.includes(i.name))}
          onclick={() => all(g.items, true)}>All</Button
        >
        <Button variant="ghost" size="xs" disabled={!changeable.some((i) => value.includes(i.name))} onclick={() => all(g.items, false)}>None</Button>
      </div>
      <div class="-mx-3 grid">
        {#each g.items as item (item.name)}
          {@const own = canChange(item.name)}
          <OptionRow
            id="{id}-{item.name}"
            label={item.label}
            description={item.help + reason(item.name) + (own ? "" : " You do not hold it, so you cannot change it.")}
          >
            {#snippet control()}
              <Checkbox
                id="{id}-{item.name}"
                checked={value.includes(item.name)}
                disabled={!own}
                onCheckedChange={(v) => toggle(item.name, v === true)}
              />
            {/snippet}
          </OptionRow>
        {/each}
      </div>
    </div>
  {/each}
</div>
