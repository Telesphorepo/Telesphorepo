<!-- The flat logo mark from branding/logo-mark.svg: a ticket with the forward-leaning T punched out.
     size is the height; the mark is 64×40. Gold Deep on light themes, Gold on dark (branding/README.md). -->
<script lang="ts">
  let { size = 28, class: className = "" }: { size?: number; class?: string } = $props();
</script>

<svg
  width={(size * 64) / 40}
  height={size}
  viewBox="0 0 64 40"
  class="text-[#A8760F] dark:text-[#E9B546] {className}"
  aria-hidden="true"
  focusable="false"
>
  <path
    fill="currentColor"
    fill-rule="evenodd"
    d="M4 0H60A4 4 0 0 1 64 4V15A5 5 0 0 0 64 25V36A4 4 0 0 1 60 40H4A4 4 0 0 1 0 36V25A5 5 0 0 0 0 15V4A4 4 0 0 1 4 0ZM12 6H40L33 13H26V27L19 34V13H5ZM44 4A1 1 0 0 1 46 4V6A1 1 0 0 1 44 6ZM44 10A1 1 0 0 1 46 10V12A1 1 0 0 1 44 12ZM44 16A1 1 0 0 1 46 16V18A1 1 0 0 1 44 18ZM44 22A1 1 0 0 1 46 22V24A1 1 0 0 1 44 24ZM44 28A1 1 0 0 1 46 28V30A1 1 0 0 1 44 30ZM44 34A1 1 0 0 1 46 34V36A1 1 0 0 1 44 36Z"
  />
</svg>
