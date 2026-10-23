<!--
  A person's avatar, generated from the username (branding/README.md, Avatars): a circle in one of
  six brand hues (--avatar-1..6), one corner cut off in a darker shade along a 45-degree line (the
  angle of the T's stem), and the initials on top in --avatar-ink. A hash of the username picks the
  hue, the corner and the depth of the cut, so the same person has the same avatar on every page.
  The name always sits next to it, so it is hidden from screen readers.

    <UserAvatar name={u.username} />            24px
    <UserAvatar name={u.username} size="md" />  32px: the rows of a list, the account menu
    <UserAvatar name={u.username} size="lg" />  40px: an identity card
-->
<script lang="ts">
  import { cn } from "$lib/utils";

  let { name, size = "sm" }: { name: string; size?: "sm" | "md" | "lg" } = $props();

  /** FNV-1a: a stable 32-bit hash of the username. */
  function hash(s: string): number {
    let h = 0x811c9dc5;
    for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 0x01000193);
    return h >>> 0;
  }

  /** "priya" → "PR", "jonas.berg" → "JB". */
  function initialsOf(s: string): string {
    const parts = s.split(/[\s._-]+/).filter(Boolean);
    const t = parts.length > 1 ? parts[0][0] + parts[1][0] : (parts[0] ?? "?").slice(0, 2);
    return t.toUpperCase();
  }

  const h = $derived(hash(name || "?"));
  const hue = $derived((h % 6) + 1);
  const corner = $derived(((h >>> 4) % 4) * 90);
  // The cut runs at 45 degrees, the angle of the T's stem, from 14, 18 or 22 units along two edges,
  // so the shade never reaches the middle where the initials sit.
  const cut = $derived(18 + (((h >>> 8) % 3) - 1) * 4);
</script>

<span
  aria-hidden="true"
  class={cn(
    "relative inline-flex shrink-0 items-center justify-center overflow-hidden rounded-full font-semibold text-(--avatar-ink) select-none",
    "after:absolute after:inset-0 after:rounded-full after:ring-1 after:ring-foreground/10 after:ring-inset",
    size === "sm" ? "size-6 text-[0.5625rem] tracking-[0.02em]" : size === "md" ? "size-8 text-[0.6875rem]" : "size-10 text-[0.8125rem]",
  )}
>
  <svg viewBox="0 0 40 40" class="absolute inset-0 size-full" focusable="false">
    <g transform="rotate({corner} 20 20)">
      <rect width="40" height="40" fill="var(--avatar-{hue})" />
      <polygon points="{40 - cut},40 40,{40 - cut} 40,40" fill="var(--avatar-ink)" fill-opacity=".2" />
    </g>
  </svg>
  <span class="relative leading-none">{initialsOf(name || "?")}</span>
</span>
