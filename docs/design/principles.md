# Telesphorepo design principles

Telesphorepo exists because the tools it replaces feel like 2015: dense, flat forms where every
setting weighs the same and nothing can be scanned. The design is the point of the project. These
principles decide every screen. `docs/design/rationale.md` records each decision with the problem it
solves and before/after screenshots.

## 1. State lives where the thing is

A page never opens with a summary or status row that describes what is below it. State shows on
the thing itself: a switch that is on, a method listed under two-step verification, a lock on a
private repository, a badge on the one row that needs attention. Anything that needs attention is
the first thing in the reading order, as itself, not as a sentence about it.

## 2. Group by task, not by setting key

Settings are grouped by what a person is trying to do, in the order they do it. A setup becomes
numbered steps. A page that connects to another system has a **Test** action where the connection
is configured; its result appears beside that action and only when there is something to report.

Example: **Single sign-on** is three steps: Connect your provider → Choose who becomes an admin →
Sign-in options, with **Test** in the first step.

## 3. Empty states are invitations

An empty list or a feature that is not set up never leaves a large blank area. It is a compact row
or card: a small illustration or icon, one sentence on why it is useful, and one primary action.

## 4. One form grid

Every form uses the card field grid of principle 12: labels above controls, two columns, every
control filling its cell, so every text control in a grid has the same width. A
number with a unit is one control of that same total width. Yes/no settings are switches at the
start of the control column. Longer help goes behind a "Learn more" disclosure. Every Settings page
uses the same content width, and tables use all of it, with zebra stripes, one fixed row height
(long values truncate with a tooltip), and right-aligned numbers. The page reserves its scrollbar
gutter (`scrollbar-gutter: stable`) so layouts never shift. (The owner rejected content-sized
controls: mismatched widths read as broken.)

## 5. A real scale

One type scale (page title, section title, row label, body, meta) and one spacing scale (4px base).
Color means state. Gold is reserved for the primary action and the ticket moments. Monospace appears
only in code blocks and copy-paste snippets.

## 6. Moments of delight that carry the idea

The product's idea is a ticket: the server issues a short-lived ticket for each publish. Small
animations make that idea visible. They are short (under 400 ms), never block work, and respect
`prefers-reduced-motion`.

- Creating a publish session prints a felt ticket.
- Closing a session punches it.
- Approving a device tears the stub.
- A finished Nexus cut-over stamps "Cut over".
- Copy buttons confirm with a small check.

## 7. Every state is designed

Loading, empty, error, success, partial and disabled states exist for every view. Errors say what
happened and what to do next, in plain words.

## 8. Say it once, show it instead of describing it

Never describe what the screen already shows ("this repository is public, no credentials
needed"): convey state through icons and behavior (a private repository shows a lock; a public one
simply has no credentials step). Never state the same fact twice (a "Public" pill and a banner
that says it again). Text explains only what icons and layout cannot.

## 9. Icons with meaning, no decoration

Each format plugin supplies its own SVG icon (Maven, Gradle, Debian logos; raw a simple file
glyph), so a new format brings its icon. Icons mark meaning (format, lock, state); they are never
decoration. No colored icons in tinted squares beside headings or rows.

## 10. Dialogs for actions, pages for places

An action that does not need to be on screen all the time opens in a dialog (change password, add
a person, create a token). A page section never holds a lone action button. Long, multi-step
flows get their own page.

## 11. No skeletons, no layout shift

Loading never shows skeletons. Show nothing for the first 300 ms; after that, a quiet progress
indicator in the space the content will take. Keep the previous content visible while a revisit
refreshes. Reserve the final size so nothing jumps; switching views keeps the same area.

## 12. Logical containers

Each settings area is a card: a header with its title (and the area's actions, if any, at the
header's end), then its fields. No status row. Cards stack with even space between them, so
the eye takes one group at a time. Inside a card, fields sit in a two-column grid with the label
above each control and every control filling its cell, so widths are uniform without a separate
label column; a field that needs the full width spans both columns. Thin dividers separate
sub-groups inside a card only, never across the page. On narrow screens the grid becomes one column.

## 13. Every path a person might think of

People start a task from different places. One person adds a colleague to a repository from the
repository; another starts from the person. Both paths exist and lead to the same controls: a
person's page can grant access to a repository, and a repository's Access tab can add a CI
pipeline. Each person and each pipeline has its own page. This is not repetition: repetition is
the same fact shown twice on one page. A page that only links to another page with nothing of its
own does not exist.

## 14. Help text stands alone

Text under a control is an explanation in a complete sentence that a person can read on its own.
It never finishes a sentence that the label starts ("Devices last" → "Then the developer approves
the device again."). If the label needs the consequence, the label says it. Help text, notes and
"Learn more" text use the same width as the controls around them, so lines never wrap early.

## 15. Nothing without a reason

Every element on a page can be defended: what it tells the person, or what it lets them do. A pill
or badge that repeats what the person already knows is clutter (an "Administrator" pill on the
administrator's own page). The account Overview has one identity card, with the person's name
and role. It has no other status. Controls of the same kind look the same: two "Add" buttons in
one list share one style. The whole row of a checkbox or a choice is its hit target, not only the
box or the title. An inline editor that opens has a visible way to close it (Cancel, or the
action completes), and a short form opens in a dialog. Anything a person can remove, they can
add back from the same place.

## Process

Each page is designed against the running app. A screenshot follows every change; a review checks
the page against these principles before the next page starts.
