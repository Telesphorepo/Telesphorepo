<!--
  The brand's felt ticket (branding/ticket-skeuomorphic.svg: the flat mark's geometry x10 in matte
  wool felt) with live printed lines, for the ticket moments of principle 6. Use it only at 320px
  wide or more (the brand minimum for the felt) and never for decoration.

    <FeltTicket top="acme-releases" bottom="Works until 14:32" serial="No 000042" moment="print" />

  `moment` plays once when it is set:
  - `print`: the ticket feeds out of a slot, as from a ticket printer (creating a publish token).
  - `punch`: a hole is punched through the stub and the felt fades (closing a publish token).
  - `tear`: the stub tears off along the perforation (approving a device).
  Each takes under 400 ms. With prefers-reduced-motion the end state shows at once.
-->
<script lang="ts" module>
  export type TicketMoment = "none" | "print" | "punch" | "tear";
</script>

<script lang="ts">
  let {
    top,
    bottom,
    serial,
    moment = "none",
    label,
  }: {
    /** The line printed above the T. */
    top: string;
    /** The line printed under the T. */
    bottom: string;
    /** Printed down the stub. */
    serial: string;
    moment?: TicketMoment;
    /** The accessible name of the picture. */
    label: string;
  } = $props();

  const uid = $props.id();
  const BODY = "M80 32H640A40 40 0 0 1 680 72V182A50 50 0 0 0 680 282V392A40 40 0 0 1 640 432H80A40 40 0 0 1 40 392V282A50 50 0 0 0 40 182V72A40 40 0 0 1 80 32ZM480 72A10 10 0 0 1 500 72V92A10 10 0 0 1 480 92ZM480 132A10 10 0 0 1 500 132V152A10 10 0 0 1 480 152ZM480 192A10 10 0 0 1 500 192V212A10 10 0 0 1 480 212ZM480 252A10 10 0 0 1 500 252V272A10 10 0 0 1 480 272ZM480 312A10 10 0 0 1 500 312V332A10 10 0 0 1 480 332ZM480 372A10 10 0 0 1 500 372V392A10 10 0 0 1 480 392Z";
  const SLOTS = [72, 132, 192, 252, 312, 372].map((y) => `M480 ${y}A10 10 0 0 1 500 ${y}V${y + 20}A10 10 0 0 1 480 ${y + 20}Z`);
  const PANEL = "M78 46H428A24 24 0 0 1 452 70V394A24 24 0 0 1 428 418H78A24 24 0 0 1 54 394V294.45A64 64 0 0 0 54 169.55V70A24 24 0 0 1 78 46Z";
  const STUB = "M552 46H642A24 24 0 0 1 666 70V169.55A64 64 0 0 0 666 294.45V394A24 24 0 0 1 642 418H552A24 24 0 0 1 528 394V70A24 24 0 0 1 552 46Z";
  const T = "M160 92L440 92L370 162L300 162L300 302L230 372L230 162L90 162Z";
  // Long lines shrink to fit the panel (about 360 units wide).
  const fit = (s: string, size: number) => Math.min(size, Math.floor(360 / Math.max(1, s.length * 0.62)));
</script>

<!-- `clip` cuts the ticket into a piece before the shadow and the felt are drawn, so each torn piece
     casts its own shadow and gets a soft felt edge along the tear. -->
{#snippet felt(clip?: string)}
  <g filter="url(#{uid}-shadow)"><path fill="#000" fill-rule="evenodd" d={BODY} clip-path={clip} /></g>
  <g filter="url(#{uid}-felt)"><path fill="#000" fill-rule="evenodd" d={BODY} clip-path={clip} /></g>
  <g clip-path={clip}>
  <g fill="#000" filter="url(#{uid}-holes)" clip-path="url(#{uid}-clip)">
    {#each SLOTS as d (d)}<path {d} />{/each}
  </g>
  <g filter="url(#{uid}-ink)" fill="none" stroke="#4E3307" stroke-opacity=".42" stroke-width="2">
    <path d={PANEL} />
    <path d={STUB} />
  </g>
  <g filter="url(#{uid}-ink)" fill="#4E3307" fill-opacity=".78" font-family="'Geist Variable', ui-sans-serif, sans-serif" font-weight="600" text-anchor="middle">
    <text x="253" y="78" font-size={fit(top, 24)} letter-spacing="1.5">{top}</text>
    <text x="253" y="404" font-size={fit(bottom, 20)} letter-spacing="1.5">{bottom}</text>
    <text x="597" y="232" font-size="22" letter-spacing="3" dominant-baseline="middle" transform="rotate(-90 597 232)">{serial}</text>
  </g>
  <path fill="#000" filter="url(#{uid}-press)" d={T} />
  </g>
{/snippet}

<div class="ticket ticket-{moment}">
  <div class="slot" aria-hidden="true"></div>
  <div class="feed">
  <svg viewBox="0 0 720 480" role="img" aria-label={label} class="block h-auto w-full overflow-visible">
    <defs>
      <filter id="{uid}-shadow" x="-20%" y="-20%" width="140%" height="150%" color-interpolation-filters="sRGB">
      <feGaussianBlur in="SourceAlpha" stdDeviation="16" result="b1"/><feOffset in="b1" dy="18" result="o1"/>
      <feFlood flood-color="#05071A" flood-opacity=".5"/><feComposite in2="o1" operator="in" result="s1"/>
      <feGaussianBlur in="SourceAlpha" stdDeviation="3" result="b2"/><feOffset in="b2" dy="3" result="o2"/>
      <feFlood flood-color="#05071A" flood-opacity=".45"/><feComposite in2="o2" operator="in" result="s2"/>
      <feMerge><feMergeNode in="s1"/><feMergeNode in="s2"/></feMerge></filter>
      <filter id="{uid}-felt" x="-2%" y="-2%" width="104%" height="104%" color-interpolation-filters="sRGB">
      <feTurbulence type="fractalNoise" baseFrequency=".9" numOctaves="2" seed="4" result="fz"/>
      <feDisplacementMap in="SourceAlpha" in2="fz" scale="2.6" xChannelSelector="R" yChannelSelector="G" result="cut"/>
      <feTurbulence type="turbulence" baseFrequency=".55" numOctaves="2" seed="9" result="hz"/>
      <feDisplacementMap in="SourceAlpha" in2="hz" scale="7" xChannelSelector="R" yChannelSelector="B" result="hairy"/>
      <feGaussianBlur in="hairy" stdDeviation=".7" result="hairb"/>
      <feComponentTransfer in="hairb" result="halo"><feFuncA type="linear" slope=".4"/></feComponentTransfer>
      <feGaussianBlur in="cut" stdDeviation=".45" result="edge"/>
      <feMerge result="alpha"><feMergeNode in="halo"/><feMergeNode in="edge"/></feMerge>
      <feTurbulence type="fractalNoise" baseFrequency=".5" numOctaves="4" seed="21" result="fine"/>
      <feGaussianBlur in="fine" stdDeviation=".6" result="fines"/>
      <feTurbulence type="fractalNoise" baseFrequency=".014" numOctaves="2" seed="2" result="mottle"/>
      <feComposite in="fines" in2="mottle" operator="arithmetic" k2="1" k3=".7" k4="-.35" result="height"/>
      <feDiffuseLighting in="height" surfaceScale="1.1" diffuseConstant="1" lighting-color="#F7C766" result="lit"><feDistantLight azimuth="225" elevation="62"/></feDiffuseLighting>
      <feTurbulence type="turbulence" baseFrequency=".2" numOctaves="2" seed="31" result="v1"/>
      <feColorMatrix in="v1" type="matrix" values="0 0 0 0 1  0 0 0 0 .92  0 0 0 0 .72  -6 0 0 0 1.05" result="h1"/>
      <feGaussianBlur in="h1" stdDeviation=".35" result="h1b"/><feComponentTransfer in="h1b" result="hairlight"><feFuncA type="linear" slope=".3"/></feComponentTransfer>
      <feTurbulence type="turbulence" baseFrequency=".26" numOctaves="2" seed="37" result="v2"/>
      <feColorMatrix in="v2" type="matrix" values="0 0 0 0 .45  0 0 0 0 .28  0 0 0 0 .04  -7 0 0 0 1.05" result="h2"/>
      <feGaussianBlur in="h2" stdDeviation=".35" result="h2b"/><feComponentTransfer in="h2b" result="hairdark"><feFuncA type="linear" slope=".14"/></feComponentTransfer>
      <feComponentTransfer in="edge" result="inv"><feFuncA type="table" tableValues="1 0"/></feComponentTransfer>
      <feGaussianBlur in="inv" stdDeviation="5" result="invb"/>
      <feFlood flood-color="#6B4508" flood-opacity=".35"/><feComposite in2="invb" operator="in"/><feComposite in2="edge" operator="in" result="rimshade"/>
      <feMerge result="surf"><feMergeNode in="lit"/><feMergeNode in="hairdark"/><feMergeNode in="hairlight"/><feMergeNode in="rimshade"/></feMerge>
      <feComposite in="surf" in2="alpha" operator="in"/></filter>
      <filter id="{uid}-press" x="-10%" y="-10%" width="120%" height="120%" color-interpolation-filters="sRGB">
      <feTurbulence type="fractalNoise" baseFrequency=".9" numOctaves="2" seed="13" result="fz"/>
      <feDisplacementMap in="SourceAlpha" in2="fz" scale="2" xChannelSelector="R" yChannelSelector="G" result="cut"/>
      <feGaussianBlur in="cut" stdDeviation="1.4" result="t"/>
      <feComponentTransfer in="t" result="inv"><feFuncA type="table" tableValues="1 0"/></feComponentTransfer>
      <feOffset in="inv" dx="4" dy="5" result="so"/><feGaussianBlur in="so" stdDeviation="4.5" result="sb"/>
      <feFlood flood-color="#4A2E04" flood-opacity=".75"/><feComposite in2="sb" operator="in"/>
      <feComposite in2="t" operator="in" result="inner"/>
      <feOffset in="inv" dx="-3" dy="-3.5" result="ho"/><feGaussianBlur in="ho" stdDeviation="2.5" result="hb"/>
      <feFlood flood-color="#FFE3A0" flood-opacity=".45"/><feComposite in2="hb" operator="in"/>
      <feComposite in2="t" operator="in" result="innerhi"/>
      <feFlood flood-color="#7A520E" flood-opacity=".3"/><feComposite in2="t" operator="in" result="floor"/>
      <feOffset in="t" dx="-2.5" dy="-3" result="ro"/><feComposite in="ro" in2="t" operator="out" result="rim"/>
      <feGaussianBlur in="rim" stdDeviation="2.2" result="rimb"/>
      <feFlood flood-color="#5A3906" flood-opacity=".22"/><feComposite in2="rimb" operator="in" result="lipshade"/>
      <feOffset in="t" dx="2.5" dy="3" result="ro2"/><feComposite in="ro2" in2="t" operator="out" result="rim2"/>
      <feGaussianBlur in="rim2" stdDeviation="2.2" result="rimb2"/>
      <feFlood flood-color="#FFE7AE" flood-opacity=".3"/><feComposite in2="rimb2" operator="in" result="lip"/>
      <feMerge><feMergeNode in="lipshade"/><feMergeNode in="lip"/><feMergeNode in="floor"/><feMergeNode in="inner"/><feMergeNode in="innerhi"/></feMerge></filter>
      <filter id="{uid}-holes" x="-50%" y="-20%" width="200%" height="140%" color-interpolation-filters="sRGB">
      <feMorphology in="SourceAlpha" operator="dilate" radius="3" result="d"/>
      <feGaussianBlur in="d" stdDeviation="2.5" result="db"/>
      <feComposite in="db" in2="SourceAlpha" operator="out" result="ring"/>
      <feFlood flood-color="#4A2E04" flood-opacity=".4"/><feComposite in2="ring" operator="in"/></filter>
      <filter id="{uid}-ink" x="-5%" y="-5%" width="110%" height="110%" color-interpolation-filters="sRGB">
      <feTurbulence type="fractalNoise" baseFrequency=".8" numOctaves="2" seed="11" result="n"/>
      <feDisplacementMap in="SourceGraphic" in2="n" scale="1.6" xChannelSelector="R" yChannelSelector="G" result="d"/>
      <feGaussianBlur in="d" stdDeviation=".55" result="soft"/>
      <feTurbulence type="fractalNoise" baseFrequency=".06" numOctaves="2" seed="17" result="m"/>
      <feColorMatrix in="m" type="matrix" values="0 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 .9 .45" result="dens"/>
      <feComposite in="soft" in2="dens" operator="in"/></filter>
      <clipPath id="{uid}-clip"><path fill-rule="evenodd" clip-rule="evenodd" d={BODY} /></clipPath>
      <clipPath id="{uid}-main"><rect x="0" y="0" width="490" height="480" /></clipPath>
      <clipPath id="{uid}-stub"><rect x="490" y="0" width="230" height="480" /></clipPath>
      <mask id="{uid}-punch" maskUnits="userSpaceOnUse" x="0" y="0" width="720" height="480">
        <rect width="720" height="480" fill="#fff" />
        <circle class="hole" cx="597" cy="100" r="19" fill="#000" />
      </mask>
    </defs>
    {#if moment === "tear"}
      <g>{@render felt(`url(#${uid}-main)`)}</g>
      <g class="stub">{@render felt(`url(#${uid}-stub)`)}</g>
    {:else}
      <g class="body" mask={moment === "punch" ? `url(#${uid}-punch)` : undefined}>{@render felt()}</g>
    {/if}
  </svg>
  </div>
</div>

<style>
  .ticket {
    width: 100%;
    min-width: 20rem;
    max-width: 24rem;
  }

  /* Print: the ticket feeds down out of the slot in two pushes, like a ticket printer. */
  .ticket-print svg {
    animation: print 380ms cubic-bezier(0.25, 0.7, 0.35, 1) both;
  }
  .ticket-print .feed {
    overflow: hidden;
  }
  @keyframes print {
    0% {
      transform: translateY(-100%);
    }
    45% {
      transform: translateY(-50%);
    }
    58% {
      transform: translateY(-46%);
    }
    100% {
      transform: translateY(0);
    }
  }
  /* The printer's slot: a dark line the ticket comes out of; it fades once the ticket is out. */
  .slot {
    height: 6px;
    margin: 0 4%;
    border-radius: 9999px;
    background: var(--foreground);
    opacity: 0;
  }
  :global(.dark) .slot {
    background: oklch(0.06 0.01 270);
    box-shadow: 0 0 0 1px var(--border);
  }
  .ticket-print .slot {
    animation: slot 400ms ease-out both;
  }
  @keyframes slot {
    0%,
    60% {
      opacity: 0.85;
    }
    100% {
      opacity: 0;
    }
  }
  /* Punch: the hole opens with a small jolt, then the felt goes dull. */
  .ticket-punch .hole {
    transform-box: fill-box;
    transform-origin: center;
    animation: punch 220ms cubic-bezier(0.2, 1.4, 0.4, 1) both;
  }
  .ticket-punch svg {
    animation: jolt 220ms ease-out;
    filter: saturate(0.35) brightness(0.92);
    opacity: 0.85;
    transition:
      filter 300ms ease-out 120ms,
      opacity 300ms ease-out 120ms;
  }
  @keyframes punch {
    from {
      transform: scale(0);
    }
    to {
      transform: scale(1);
    }
  }
  @keyframes jolt {
    40% {
      transform: translateY(3px);
    }
  }

  /* Tear: the stub comes away along the perforation and drops a little. */
  .ticket-tear .stub {
    transform-box: view-box;
    transform-origin: 490px 440px;
    animation: tear 380ms cubic-bezier(0.3, 0.6, 0.3, 1) both;
  }
  @keyframes tear {
    0% {
      transform: none;
    }
    30% {
      transform: translate(2px, 1px) rotate(1.5deg);
    }
    100% {
      transform: translate(30px, 26px) rotate(9deg);
    }
  }
</style>
