<!--
  Permissions as checkboxes in groups: each group has a title with All and None, and each
  checkbox has one line of help. `needs` holds the dependency rules: a check also checks what the
  permission needs, a clear also clears what needs it, and the help line of a permission that
  another one needs says why it is checked. A permission outside `held` cannot change, because the
  server lets a person give and remove only the permissions they hold.

    <PermissionPicker id="p" groups={[{ title: "Permissions", items: REPO_PERMISSIONS }]}
      needs={REPO_NEEDS} held={mine} bind:value={perms} />
-->
<script lang="ts" generics="P extends string">
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import OptionRow from "./OptionRow.svelte";
  import type { PermissionInfo } from "$lib/permissions";
  import { cn } from "$lib/utils";

  let {
    id,
    groups,
    value = $bindable([]),
    needs = {},
    held,
    columns = 1,
    onchange,
  }: {
    /** Prefix of the element ids, unique on the page. */
    id: string;
    groups: { title: string; items: PermissionInfo<P>[] }[];
    value?: P[];
    /** A permission and the permission it needs. */
    needs?: Partial<Record<P, P>>;
    /** The permissions the person can change; undefined: every one. */
    held?: readonly P[];
    /** Two columns on a page from 640px; one in a dialog. */
    columns?: 1 | 2;
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
      for (let x: P | undefined = p; x && !out.has(x); x = needs[x]) out.add(x);
      return;
    }
    if (!out.delete(p)) return;
    for (const k of Object.keys(needs) as P[]) if (needs[k] === p) apply(out, k, false);
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
    const by = (Object.keys(needs) as P[]).filter((k) => needs[k] === p && value.includes(k));
    return by.length ? ` ${by.map(labelOf).join(" and ")} ${by.length === 1 ? "needs" : "need"} it.` : "";
  }
</script>

<div class="flex flex-col gap-4">
  {#each groups as g, gi (g.title)}
    {@const changeable = g.items.filter((i) => canChange(i.name))}
    <div class="flex flex-col gap-1" role="group" aria-labelledby="{id}-g{gi}">
      <div class="flex items-center gap-1">
        <h3 id="{id}-g{gi}" class="text-label">{g.title}</h3>
        <Button
          class="ml-auto"
          variant="ghost"
          size="xs"
          disabled={changeable.every((i) => value.includes(i.name))}
          onclick={() => all(g.items, true)}>All</Button
        >
        <Button variant="ghost" size="xs" disabled={!changeable.some((i) => value.includes(i.name))} onclick={() => all(g.items, false)}>None</Button>
      </div>
      <div class={cn("-mx-3 grid", columns === 2 && "sm:grid-cols-2")}>
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
