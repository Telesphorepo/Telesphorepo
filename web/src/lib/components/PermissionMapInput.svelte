<!--
  A map of a value of the provider's group field to server permissions (the setting
  oidc.permission_map): one row per group with the phrase of its permissions, Change and Remove,
  and Add a group. Add and Change open a short form with the group and a PermissionPicker of the
  server permissions, grouped by subsystem with their labels and help.

    <PermissionMapInput id="map" value={map} onchange={(v) => (map = v)} />
-->
<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import CompactEmpty from "./CompactEmpty.svelte";
  import ShortFormDialog from "./ShortFormDialog.svelte";
  import FormField from "./FormField.svelte";
  import PermissionPicker from "./PermissionPicker.svelte";
  import { needsOf, pickerGroups, serverPhrase } from "$lib/permissions";
  import type { PermId } from "$lib/api";
  import UsersIcon from "@lucide/svelte/icons/users-round";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let {
    id,
    value,
    onchange,
    disabled = false,
    describedby,
  }: {
    id: string;
    value: Record<string, PermId[]>;
    onchange: (v: Record<string, PermId[]>) => void;
    disabled?: boolean;
    describedby?: string;
  } = $props();

  const rows = $derived(Object.entries(value));

  let open = $state(false);
  // The group the form edits; "" adds one.
  let editing = $state("");
  let group = $state("");
  let perms = $state<PermId[]>([]);

  function edit(g: string) {
    editing = g;
    group = g;
    perms = [...(value[g] ?? [])];
    open = true;
  }

  const taken = $derived(group.trim() !== editing && group.trim() in value);

  function save() {
    const next: Record<string, PermId[]> = {};
    for (const [k, v] of Object.entries(value)) next[k === editing ? group.trim() : k] = k === editing ? perms : v;
    if (!editing) next[group.trim()] = perms;
    onchange(next);
  }

  function remove(g: string) {
    const { [g]: _, ...rest } = value;
    onchange(rest);
  }
</script>

<div {id} class="flex flex-col gap-3" aria-describedby={describedby}>
  {#if rows.length}
    <ul class="flex flex-col divide-y rounded-lg border">
      {#each rows as [g, ps] (g)}
        <li class="flex h-row items-center gap-3 px-3 text-body">
          <span class="min-w-0 truncate font-medium" title={g}>{g}</span>
          <span class="min-w-0 flex-1 truncate text-muted-foreground">{serverPhrase(ps) || "No permissions"}</span>
          <Button variant="ghost" size="sm" {disabled} onclick={() => edit(g)}>Change</Button>
          <Button variant="ghost" size="sm" class="hover:bg-destructive/10 hover:text-destructive" {disabled} onclick={() => remove(g)}>Remove</Button>
        </li>
      {/each}
    </ul>
  {:else}
    <CompactEmpty icon={UsersIcon} title="Groups" state="None yet" text="A person in a group listed here gets its permissions at each sign-in." />
  {/if}
  <div>
    <Button variant="outline" size="sm" {disabled} onclick={() => edit("")}><PlusIcon />Add a group</Button>
  </div>
</div>

<ShortFormDialog
  bind:open
  title={editing ? `Change ${editing}` : "Add a group"}
  submitLabel={editing ? "Change permissions" : "Add group"}
  canSubmit={group.trim() !== "" && !taken}
  onSubmit={save}
>
  <FormField label="Group" for="{id}-group" hint="The value in the group field of the sign-in token, for example repo-admins." error={taken ? "This group is listed already." : undefined}>
    <Input id="{id}-group" autocomplete="off" spellcheck={false} bind:value={group} />
  </FormField>
  <FormField label="Server permissions">
    <PermissionPicker id="{id}-perms" groups={pickerGroups("server")} needs={needsOf("server")} bind:value={perms} />
  </FormField>
</ShortFormDialog>
