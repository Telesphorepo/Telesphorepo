<!--
  Who acted, as the mark at the start of an event: a person's UserAvatar; the server itself, its
  command line or a CI job the brand's mark; someone without an account a plain person icon.
  `sm` is 24px (the rows of Activity), `md` 32px (an ItemList row).
-->
<script lang="ts">
  import UserAvatar from "$lib/components/UserAvatar.svelte";
  import Logo from "$lib/components/Logo.svelte";
  import CircleUserRoundIcon from "@lucide/svelte/icons/circle-user-round";
  import type { Actor } from "./wording";

  let { actor, size = "sm" }: { actor: Actor; size?: "sm" | "md" } = $props();
</script>

{#if actor.kind === "person"}
  <UserAvatar name={actor.name} {size} />
{:else}
  <span class={["flex shrink-0 items-center justify-center text-muted-foreground", size === "sm" ? "size-6" : "size-8"]}>
    {#if actor.kind === "someone"}<CircleUserRoundIcon class="size-5" />{:else}<Logo size={size === "sm" ? 14 : 20} />{/if}
  </span>
{/if}
