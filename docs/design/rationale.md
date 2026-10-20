# Design rationale

Each entry names a problem, the decision that solves it, and the principle from
[principles.md](principles.md) that the decision applies. The screenshots are in
[screenshots/](screenshots/). A "before" shot shows the page before the redesign. An "after" shot
shows the page as built, at 1440 px wide or at 390 px (a phone), in dark or light. A new feature has
no "before" shot, and the entry says so.

The components named here are described in [web/src/README.md](../../web/src/README.md).

## Decisions that apply to every page

### No status row

Problem: most pages opened with a banner that described the page below it ("Two-step verification:
Off", "Identity provider: Connected", "Storage: Local disk"). The banner said again what the page
showed, and it pushed the real content down.

Decision: no page has a status row or a summary banner. State shows on the thing itself:

- A switch shows that it is on.
- A method shows in its list.
- A private repository has a lock after its name.
- A running storage move is its own card, with progress and **Cancel**.
- A Nexus server shows its state as a badge by its title.
- Two-step verification shows on Sign-in and security, as the list of methods: a method that is
  not set up is a row with **Add**.

Principle: 1. State lives where the thing is.

Before: [sso-before-1440-dark.jpg](screenshots/sso-before-1440-dark.jpg),
[security-before-1440-dark.jpg](screenshots/security-before-1440-dark.jpg),
[general-before-1440-dark.jpg](screenshots/general-before-1440-dark.jpg).
After: [sign-in-options-default-1440-dark.png](screenshots/sign-in-options-default-1440-dark.png),
[account-security-default-1440-dark.png](screenshots/account-security-default-1440-dark.png),
[account-overview-default-1440-dark.png](screenshots/account-overview-default-1440-dark.png).

### Each area is a card

Problem: a settings page was a flat wall of fields. Every setting weighed the same, and the eye had
no group to hold.

Decision: `Section` puts each area on its own surface (a shadcn Card): a header with the title and
the actions of the area, then the content. Cards are 24 px apart, with 20 px padding on a phone.
Every table is in a card too. Thin dividers separate sub-groups inside a card and nowhere else.

Principle: 12. Logical containers.

Before: [form-general-before-1440-dark.png](screenshots/form-general-before-1440-dark.png).
After: [system-settings-cards-1440-dark.png](screenshots/system-settings-cards-1440-dark.png),
[system-settings-cards-1440-light.png](screenshots/system-settings-cards-1440-light.png),
[system-settings-cards-390-dark.png](screenshots/system-settings-cards-390-dark.png).

### One form grid: two columns, label above the control

Problem: a 14 rem label column left a gap that made a short control look lost. Controls had content
widths, and mismatched widths read as broken.

Decision: a form is a two-column grid in a card. The label sits above its control, and every control
fills its cell, so all text controls have the same width. A field that needs the full width (a JWKS
address, a private key, a description) spans both columns. A yes/no setting is a switch at the start
of its cell. The grid is one column on a phone. A badge beside a label ("Set by TR_BASE_URL") does
not change the row height, so controls in one row line up.

Principle: 4. One form grid, 12. Logical containers.

After: [system-settings-cards-1440-dark.png](screenshots/system-settings-cards-1440-dark.png),
[system-settings-cards-390-light.png](screenshots/system-settings-cards-390-light.png).

### Test is where the connection is set

Problem: **Test** sat at the bottom of the page, after **Save**, and a banner kept a result that
nobody asked for.

Decision: `ConnectionTest` is a **Test** button in the footer (or the header) of the card or step
that sets the connection. Its result shows beside the button, and only after a test. On Sign-in
options, the page tests the saved settings when it opens and shows a result only when the test
fails. The button says **Test your edits** when the form has unsaved edits.

Principle: 2. Group by task.

Before: [sso-before-1440-dark-bottom.jpg](screenshots/sso-before-1440-dark-bottom.jpg).
After: [sign-in-options-default-1440-dark.png](screenshots/sign-in-options-default-1440-dark.png).

### A setup is numbered steps

Problem: a setup read as one long form, and a person could not tell where it ended.

Decision: `Step` is a card whose title starts with its number. A finished step shows a check in
place of the number. Sign-in options has three steps: connect your provider, choose who becomes an
administrator, sign-in options. Create a read token and add it to your build are two steps on the
Use tab of a private repository.

Principle: 2. Group by task.

After: [sign-in-options-default-390-dark.png](screenshots/sign-in-options-default-390-dark.png),
[repo-use-private-1440-dark.png](screenshots/repo-use-private-1440-dark.png).

### An empty list is one plain row

Problem: an empty list drew a full table: column headings over a tall blank area. After the first
fix, the dashed empty row repeated the title of its card, and sat in a box inside the card frame.

Decision: `DataTable` never draws an empty table; it shows its `empty` row (`CompactEmpty`) without
headings. The row is one sentence and one action. Inside a titled card the row has no dashed frame
and does not repeat the card title. The header action of the card is hidden while the list is empty,
so the row holds the only action.

Principle: 3. Empty states are invitations, 8. Say it once.

Before: [devices-before-1440-dark.jpg](screenshots/devices-before-1440-dark.jpg),
[ci-pipelines-before-1440-dark.jpg](screenshots/ci-pipelines-before-1440-dark.jpg).
After: [account-devices-empty-1440-dark.png](screenshots/account-devices-empty-1440-dark.png),
[review-ci-pipelines-empty-1440-dark.png](screenshots/review-ci-pipelines-empty-1440-dark.png),
[review-ci-pipelines-empty-390-light.png](screenshots/review-ci-pipelines-empty-390-light.png).

### Public and private are said once

Problem: a repository said "Public" in a pill, in a column, in a banner and in the Use tab.

Decision: a private repository has a lock after its name, in the repository table, the repository
header and the repository settings. A public repository has no mark. The table has no Visibility
column. The Use tab of a public repository has no credentials step, and the Publish tab shows only
to people who can publish.

Principle: 8. Say it once, show it instead of describing it.

Before: [home-before-1440-dark.png](screenshots/home-before-1440-dark.png).
After: [system-format-icons-1440-dark.png](screenshots/system-format-icons-1440-dark.png),
[repo-use-public-1440-dark.png](screenshots/repo-use-public-1440-dark.png),
[repo-use-private-1440-dark.png](screenshots/repo-use-private-1440-dark.png).

### Format icons come from the format plugin

Problem: the UI drew one Lucide icon per format, inside a tinted square. A new format plugin had
no way to bring its own icon.

Decision: a format plugin that implements `format.Iconic` supplies a one-color SVG. `GET
/api/formats` returns it inline as `icon`, and Maven also returns a Gradle icon in `kind_icons` for
Gradle plugins. `FormatIcon` draws it, 16 px by default, with its space reserved before the list
loads. `FormatBadge` is the icon and the title, with no box and no tint. Icons mark meaning (format,
lock, state) and sit bare beside headings and rows.

Principle: 9. Icons with meaning, no decoration.

No before shot: the old icons were removed with the tinted squares. After:
[system-format-icons-1440-dark.png](screenshots/system-format-icons-1440-dark.png),
[system-format-icons-1440-light.png](screenshots/system-format-icons-1440-light.png),
[system-format-icons-390-dark.png](screenshots/system-format-icons-390-dark.png). At 16 px the Maven
feather reads a little like a "V"; it is clearer at phone size.

### Loading without skeletons

Problem: skeleton rows flashed on every fast request and then jumped when the content arrived.

Decision: `Loading` shows nothing for 300 ms, then a quiet spinner in the space the content will
take. `DataTable` keeps its current rows on screen during a reload. No component draws a skeleton.

Principle: 11. No skeletons, no layout shift.

No before or after shot: the state lasts a moment on a fast server.

### A dialog for an action, a page for a place

Problem: the password form sat open on the security page, and a card held one **Delete** button.

Decision: **Change** on the password row opens a "Change your password" dialog that flags a mismatch
as you type. **Delete** is an action in the header of a repository, a CI pipeline, a network list or
a Nexus server, and it opens a confirm dialog (the type-the-name dialog for a repository). The CI
pipeline and the network list keep their own pages for **New**, because they have several sections.

Principle: 10. Dialogs for actions, pages for places.

Before: [security-before-1440-dark.jpg](screenshots/security-before-1440-dark.jpg),
[repo-overview-before-1440-dark.png](screenshots/repo-overview-before-1440-dark.png).
After: [account-security-change-password-1440-dark.png](screenshots/account-security-change-password-1440-dark.png),
[account-security-change-password-390-light.png](screenshots/account-security-change-password-390-light.png),
[repo-delete-after-1440-dark.png](screenshots/repo-delete-after-1440-dark.png).

### Bulk actions

Problem: after a Nexus import of 40 private repositories, an administrator made them public one at
a time: open the repository, open Access, turn on Public, and go back, 40 times. People, access and
files had the same problem. Each change had a route for one item, so a change of many items was
many requests, and a failure part way left some items changed and some not.

Decision: the tables of many things take a pick. `DataTable` gives each row a checkbox in a first
column, and the page names only its actions. The checkbox cell is a 40 px target over the full row
height, and a click on the rest of the row still opens the row. Shift+click picks a range, and Esc
clears the pick. While rows are picked, a bar takes the place of the header row, with the same
height, so nothing below it moves: "12 selected", the actions, "Select all 50" and "Clear". The
same verbs show everywhere: **Make public**, **Make private**, **Delete**, **Disable**,
**Enable**, **Give access**, **Remove access**. An action that the other one undoes (Make public,
Make private, Enable) runs at once. A delete asks first and names the count and the first five
names; a delete of more than one repository asks for the count. A toast says what changed ("12
repositories are now public."). On the server, one route takes the list, and one item is a list of
one. The server checks every item before it changes any, so a bad name changes nothing.

The pick is on All repositories, the Files tab (List view), People, a person's repositories and a
repository's people. Tokens, devices, network lists, signing keys and CI pipelines stay one at a
time: a person has few of each, so a pick costs more than it saves.

Principle: 13. Every path a person might think of, 15. Nothing without a reason.

New feature: no "before" shot. After:
[bulk-repos-picked-1200-light.png](screenshots/bulk-repos-picked-1200-light.png),
[bulk-repos-picked-390-light.png](screenshots/bulk-repos-picked-390-light.png) (on a phone the
bar scrolls sideways, and its right edge fades to say so),
[bulk-person-access-1200-light.png](screenshots/bulk-person-access-1200-light.png).

### Small building blocks

| Problem | Decision | Principle |
|---|---|---|
| A yes/no setting carried a paragraph and an environment variable. | `SwitchRow`: the switch and its state word in the cell, one line of help. Rare details (the break-glass variable) move to the confirm dialog where they apply. | 4. One form grid |
| Lists were bare "One per line" textareas. | `ListInput`: chips in one box. Enter or a comma adds a value, a paste adds every line, Backspace removes the last chip. | 4. One form grid |
| A secret used a different control from every other field. | `SecretInput`: a box with the size and look of an input that shows "Set · hidden" or "Not set", with Replace, Clear or Set inside it. | 4. One form grid |
| Pages moved sideways by the scrollbar width. | `scrollbar-gutter: stable` on the root. | 4. One form grid |
| The browser scrollbar did not belong to the palette. | A themed scrollbar for the page and every inner scroll area. Firefox gets the same colors through `scrollbar-color`. | 5. A real scale |
| A full-width rule under the page header and between sections sat a few pixels from the sidebar separators. | No full-width rules in the content. Space and the type scale separate the header and the cards. | 5. A real scale |
| Sizes were Tailwind steps chosen per page. Meta text was near the 4.5:1 limit. | One type scale in `app.css`: `text-title`, `text-section`, `text-label`, `text-body`, `text-meta`. Meta text is 6.2:1 on the page in light mode (dark: 7.8:1). | 5. A real scale |
| A row with three red Remove buttons shouted. | A row action that opens a confirm is a quiet ghost button that turns red under the pointer. | 5. Color means state |
| The search field kept the last query on other pages. | The top-bar field is empty everywhere except on the Search page. | 8. Say it once |
| A sub-page repeated its group item in the breadcrumb ("Settings › Repositories › All repositories › New repository"). | A sub-page of the first item of a group does not name that item, because the group step already links there. | 8. Say it once |

## Browse

### Views that keep the same area

Problem: the file list was the only view, and a deep Maven tree needed many clicks. A first Cover
Flow stacked above the list, so switching views moved everything below it.

Decision: the switcher offers **List**, **Tree** and **Columns** everywhere, and **Cover Flow** at an
artifact or a snapshot with several builds. Tree, Columns and Cover Flow fill a box of one height
(22 to 36 rem, otherwise the window height minus 24 rem), and the list keeps that height as its
minimum, so the details column and the toolbar stay put. Tree opens the folders down to the open
folder, loads each folder on first open and answers the arrow keys. Cover Flow shows the versions or
the builds as tickets in a timeline, and its caption has Open and Delete, or "Show N files".

Principle: 11. No layout shift (switching views keeps the same area).

No before shot: the Tree is new, and the old Cover Flow was replaced. After:
[browse-tree-1440-dark.png](screenshots/browse-tree-1440-dark.png),
[browse-tree-390-light.png](screenshots/browse-tree-390-light.png),
[browse-coverflow-1440-dark.png](screenshots/browse-coverflow-1440-dark.png),
[browse-coverflow-390-dark.png](screenshots/browse-coverflow-390-dark.png).

### Versions and builds for Maven and Debian

Problem: only a Maven artifact had a Versions view and a build timeline. Debian keeps every
version of a package in one pool folder, so a package with 9 snapshot builds showed 9 files and no
versions.

Decision: a version is named by its artifact folder and its version string, not by a folder. A
Debian package folder shows its versions, and `3.0~SNAPSHOT` shows its 9 builds on the same
timeline as a Maven snapshot, newest first. The builds list shows the architecture of each `.deb`
in place of the Maven classifier. The snippet of a build installs that exact version; the snippet
of a snapshot version installs the newest.

Principle: 1. State lives where the thing is (the versions and builds of a package show at the
package); 8. Say it once, show it instead of describing it (one timeline for both formats).

No before shot: Debian had no Versions view. After:
[debian-versions-1200-light.png](screenshots/debian-versions-1200-light.png),
[debian-build-1200-light.png](screenshots/debian-build-1200-light.png) (a release with one `.deb`
for each architecture).

### Use and Publish are two tabs

Problem: "Setup" mixed using a repository with publishing to it.

Decision: **Use this repository** has the address card, then one card "Add it to your build" for a
public repository, or two numbered steps for a private one (create a read token; add it to the
build, with the credentials already in the snippets). **Publish to this repository** has one card
each for From your computer, From CI and By hand. From CI is one card with two numbered parts and a
thin divider, not step cards inside a card. A person who cannot publish does not see the tab.

Principle: 2. Group by task, 8. Say it once.

Before: [setup-before-1440-light.png](screenshots/setup-before-1440-light.png).
After: [repo-use-public-1440-dark.png](screenshots/repo-use-public-1440-dark.png),
[repo-use-private-390-light.png](screenshots/repo-use-private-390-light.png),
[repo-publish-1440-dark.png](screenshots/repo-publish-1440-dark.png),
[repo-publish-390-dark.png](screenshots/repo-publish-390-dark.png).

## Your account

### Sign-in and security

Problem: nothing said whether two-step verification was on, and the first answer was a banner. The
password form was always open. Recovery codes showed a disabled button and a hint.

Decision:

- A Sign-in card with one row, "Password" and **Change**. A single sign-on account sees "Single
  sign-on" and a line that sends it to its identity provider.
- A Two-step verification card with one list of methods. A method that is not set up is one row
  with its Add action. Without a second step this card is above the Sign-in card, so the thing to
  fix comes first.
- A Recovery codes card, only when a second step exists. Codes left show as ten small bars, like the
  slots of the ticket. Three or fewer turn the warning color.
- The next step is the one gold button.

Principle: 1, 3, 10, 12.

Before: [security-before-1440-dark.jpg](screenshots/security-before-1440-dark.jpg),
[security-before-390-light.png](screenshots/security-before-390-light.png).
After: [account-security-default-1440-dark.png](screenshots/account-security-default-1440-dark.png),
[account-security-default-1440-light.png](screenshots/account-security-default-1440-light.png),
[account-security-default-390-dark.png](screenshots/account-security-default-390-dark.png).

### Overview

Problem: a summary banner gave the role and counts, "Through: Administrator" repeated on every row,
and the "You can" column made the table scroll sideways on a phone.

Decision: an Administrator badge by the title, only for an administrator. Each table is in its own
card. Administrators do not see the "Through" column. The "You can" column shows only when its
values differ between rows, so an administrator, who can do everything, does not see it.

Principle: 1, 8, 12.

Before: [profile-before-1440-dark.jpg](screenshots/profile-before-1440-dark.jpg).
After: [account-overview-default-1440-dark.png](screenshots/account-overview-default-1440-dark.png),
[account-overview-default-390-dark.png](screenshots/account-overview-default-390-dark.png),
[review-account-overview-1440-light.png](screenshots/review-account-overview-1440-light.png).

### Tokens and devices

Problem: read tokens and publish tokens were two pages, each with a table over a blank area.

Decision: one page, Tokens, with a card each. Row actions are quiet until the pointer is on them.
The "Ends" column is hidden on a phone, so the table fits at 390 px. An empty devices list is one
row, "Devices · None yet".

Principle: 3, 12.

Before: [read-tokens-before-1440-dark.jpg](screenshots/read-tokens-before-1440-dark.jpg),
[publish-tokens-before-1440-dark.jpg](screenshots/publish-tokens-before-1440-dark.jpg).
After: [account-tokens-default-1440-dark.png](screenshots/account-tokens-default-1440-dark.png),
[account-tokens-default-390-light.png](screenshots/account-tokens-default-390-light.png),
[account-devices-empty-390-dark.png](screenshots/account-devices-empty-390-dark.png).

### The publish token is a ticket

Problem: the product issues a short-lived ticket for each publish, and nothing on the page showed
it. A new token appeared as a grey box of text.

Decision: a new publish token prints as the brand's felt ticket: the repository above the T, "Works
until 9:36 PM" under it, the token number down the stub. It feeds out of a slot in two pushes in
380 ms. **End it now** punches a hole through the stub and the felt goes dull ("Token ended"). The
ticket stays until **Done**. Under `prefers-reduced-motion` it shows at once.

Principle: 6. Moments of delight that carry the idea.

Before: [publish-tokens-before-1440-dark.jpg](screenshots/publish-tokens-before-1440-dark.jpg).
After: [publish-tokens-after-1440-dark-printed.png](screenshots/publish-tokens-after-1440-dark-printed.png),
[publish-tokens-print-midframe-dark.png](screenshots/publish-tokens-print-midframe-dark.png) (the
feed, paused at 150 ms),
[publish-tokens-after-1440-dark-ended.png](screenshots/publish-tokens-after-1440-dark-ended.png),
[publish-tokens-after-390-dark-printed.png](screenshots/publish-tokens-after-390-dark-printed.png).

### Device approval

Problem: a lone code field; after the lookup, three sections of equal weight; approval ended in a
centered message.

Decision: two numbered steps (enter the code; check the device and confirm it is you). The device
shows as a felt ticket with the code down the stub, so the person compares it with the terminal.
Approving tears the stub off along the perforation in 380 ms. On a phone the ticket is 320 px wide.

Principle: 2, 6.

After: [device-approve-after-1440-dark-check.png](screenshots/device-approve-after-1440-dark-check.png),
[device-approve-after-1440-dark-approved.png](screenshots/device-approve-after-1440-dark-approved.png),
[device-approve-tear-midframe-light.png](screenshots/device-approve-tear-midframe-light.png) (the
tear, paused at 120 ms),
[device-approve-after-390-light-approved.png](screenshots/device-approve-after-390-light-approved.png).

## Settings

### Access: one place, the final state first

Problem: access was split over three unlinked pages. Nothing showed who can download and who can
publish.

Decision: the repository Access tab has a Download card and a Publish card, each split into parts by thin dividers: who, then from
where. Each add action (Add person, Add CI pipeline, Add network list, Add addresses) opens a
short form in a dialog with a title, Cancel and Add. An earlier "+ Addresses" button opened a
paste box under itself with no way to close it but the same button. Each person shows once: the
Download table lists the people who can download and not publish, and the Publish table lists the
people who can publish, who can download too.
There is no Access overview page: it only linked to each Access tab and left blank space in its
table.

Principle: 1, 8, 12, 15.

Before: [repo-access-before-1440-dark.png](screenshots/repo-access-before-1440-dark.png),
[access-overview-before-1440-dark.png](screenshots/access-overview-before-1440-dark.png).
After: [repo-access-default-1440-dark.png](screenshots/repo-access-default-1440-dark.png),
[repo-access-default-390-dark.png](screenshots/repo-access-default-390-dark.png),
[repo-access-addresses-1440-dark.jpg](screenshots/repo-access-addresses-1440-dark.jpg),
[repo-access-ci-1440-dark.jpg](screenshots/repo-access-ci-1440-dark.jpg).

### Networks

Problem: trusted proxies and repo IP lists shared one page and one set of lists. **New network
list** sat in the page header while the empty "Your lists" row had no button.

Decision: Networks holds the lists. Network and proxy holds the trusted proxies only. **New network
list** is the action of the "Your lists" card: in its header when the card has lists, in the empty
row when it has none. On a phone **Refresh** is an icon.

Principle: 3, 12.

Before: [networks-before-1440-dark.jpg](screenshots/networks-before-1440-dark.jpg),
[network-and-proxy-before-1440-dark.jpg](screenshots/network-and-proxy-before-1440-dark.jpg).
After: [networks-default-1440-dark.png](screenshots/networks-default-1440-dark.png),
[networks-default-390-light.png](screenshots/networks-default-390-light.png),
[review-networks-1440-light.png](screenshots/review-networks-1440-light.png).

### CI pipelines and People

Problem: every pipeline row had an "On" badge, and every person said "Signs in with: Password".

Decision: only a pipeline that is off has a badge ("Off") by its name; its switch says the rest. The
sign-in column of People shows only when someone uses single sign-on.

Each person has a page (`/settings/people/:name`) in place of a details column at the side. Its
identity card holds the avatar, the role and the sign-in method, with the account actions. Its
Repositories card grants, changes and removes access, so an administrator who starts from the
person reaches the same controls as one who starts from the repository.

Principle: 1, 8, 13.

Before: [ci-pipelines-before-1440-dark.jpg](screenshots/ci-pipelines-before-1440-dark.jpg),
[people-before-1440-dark.jpg](screenshots/people-before-1440-dark.jpg).
After: [review-ci-pipelines-empty-1440-dark.png](screenshots/review-ci-pipelines-empty-1440-dark.png) (no pipeline yet),
[review-people-1440-dark.png](screenshots/review-people-1440-dark.png),
[people-list-1440-dark.png](screenshots/people-list-1440-dark.png),
[people-person-1440-dark.png](screenshots/people-person-1440-dark.png),
[people-person-390-dark.png](screenshots/people-person-390-dark.png).

### Adding a CI pipeline

Problem: a new pipeline asked for raw token conditions (claims such as `workflow_ref`), which a
developer who has never read about OpenID Connect cannot follow. A removed condition could not
be added back.

Decision: four numbered steps that start from what the developer knows. 1: where the job runs
(GitHub Actions, Jenkins, another CI system). 2: which runs can publish, in that system's words:
the GitHub repository, the workflow file, branches and tags, or the Jenkins address and job
names. The page builds the conditions from the answers; the raw conditions sit behind "Edit the
token conditions", and each removed condition has its own add button. 3: the repositories it can
publish to. 4: the exact workflow or Jenkinsfile step. Started from a repository's Access tab, the
page preselects that repository and returns there after Create.

Principle: 2, 13, 14, 15.

After: [ci-pipeline-new-1440-dark.png](screenshots/ci-pipeline-new-1440-dark.png),
[ci-pipeline-new-jenkins-1440-dark.png](screenshots/ci-pipeline-new-jenkins-1440-dark.png).

### Storage and Nexus migration

Problem: a status row gave the storage state, and a move in progress was a line of text in it.

Decision: a running storage move is its own card with progress and **Cancel**. A Nexus server shows
its state as a badge by its title. **Test** is in the footer of the bucket card, or of the Location
card for a folder.

Principle: 1, 2.

Before: [storage-before-1440-dark.jpg](screenshots/storage-before-1440-dark.jpg),
[nexus-migration-before-1440-dark.jpg](screenshots/nexus-migration-before-1440-dark.jpg).
No after shot is saved for these two pages.

### Group repositories

Problem: a build listed one repository for each place its packages came from: releases,
snapshots, modules and Maven Central. Every new repository meant an edit of every build.

Decision: a group is a repository of its own kind, with one address that serves the files of its
members in order. New repository starts with a choice: a repository, a proxy of a remote
repository, or a group of repositories. A group picks its members from the repositories of its
format, and the order shows as numbers. The group's Overview holds the address for the build and
the members, with Move up, Move down, Remove and Add repositories, saved from the save bar. A
proxy that comes before a repository of your own has a warning on its row, because the remote
can shadow your files. A private member in a public group says on its row that only people with
access get its files through the group. A repository's Overview lists the groups it is in, as
links. The repository list shows a group with its mark and its member count in place of the file
count. A group has no files, so its page shows the address and links to each member.

Principle: 1, 8, 13.

This is a new feature, so it has no before shot. After:
[new-repository-kinds-1200-light.png](screenshots/new-repository-kinds-1200-light.png),
[group-overview-1200-light.png](screenshots/group-overview-1200-light.png),
[group-overview-390-light.png](screenshots/group-overview-390-light.png) (on a phone the order
numbers and the row notes give way to the move and remove buttons).

### Proxy repositories

Problem: builds went to Maven Central, a vendor's repository or a Debian mirror on every run, and
failed when that remote was down.

Decision: a proxy is a repository that fetches a file from its remote on the first request and
keeps it. New repository asks for the remote address, with the well-known remotes as one-click
choices, and optional credentials. Create checks the remote first; when the remote does not
answer, the error shows beside the button with Create anyway. The proxy's Overview shows the
remote, when it last answered or its last error, and Test the remote, where the remote is set.
The Remote tab edits the address, the credentials (shown as set or not set, never the value) and
the two times, each one number with its unit. The Files tab shows the cached files, and Delete
removes them, so the next request fetches them again. A Debian proxy says that the remote archive
signs the files, because clients keep that archive's key.

A proxy of Maven Central, and a group that holds one, give a build snippet without
`mavenCentral()`, so the build goes through the proxy and not around it.

Principle: 1, 2, 4.

This is a new feature, so it has no before shot. After:
[proxy-overview-1200-light.png](screenshots/proxy-overview-1200-light.png),
[proxy-remote-1200-light.png](screenshots/proxy-remote-1200-light.png).

### Nexus import

Problem: the page said "Copying" for every source that was not switched over, also when nothing
copied. Saving the repositories started no sync, so the first copy began one interval later. The
repository list had no "select all", no filter and no way to take a repository out. A running
sync showed file counts only, with no sign that it still moved and no history of what each sync
did. Every imported file had the import time as its date, every new repository was private, and
an imported apt repository signed with a new key, so every client failed `apt update` after the
switch-over.

Decision: the server derives one status and the page shows it once, as a badge and one line by
the title: "Choose repositories", "Syncing" with the repository that copies now and the counts,
"Up to date" with the last and the next check, "Last sync failed" with the error, or "Switched
over". The repositories to copy are a pick table: the header checkbox picks every supported
repository, a click anywhere on a row picks it, and a filter shows above ten repositories. Who
can download each one starts as Nexus does it, so the admin changes a row only to differ from
Nexus. An apt repository can keep the Nexus key; the page asks for its passphrase only when the
key has one, because Nexus never gives it. A proxy keeps its remote and asks only for the remote
password, which Nexus never gives. Groups sit in the same table after the repositories they read,
because a save sets those up first; each group names its members and the ones it leaves out, and
picking a group picks its members. Each row says what a save does to it (Copied, New repository,
Stops copying), and the repositories this server cannot copy stay at the end, muted, with their
reason. One button, "Copy N repositories", saves and starts the copy and opens it, so
the next step is the same click. The sync shows per repository what it copied of what it
checked, the last activity, and a bar only when an earlier sync gives a total to divide by; a
repository with no progress for two minutes says so in the warning tone. The log names each step
in a line and follows the newest line, and a click on a repository filters it. The history shows
how each sync started and what it found, so a run of scheduled syncs with no new files reads as
a healthy mirror. Imported files keep their Nexus dates.

A log line of one repository does not repeat the repository's name: the row shows it in its
own column.

Principle: 1, 7, 8, 15.

Before: [nexus-migration-before-1440-dark.jpg](screenshots/nexus-migration-before-1440-dark.jpg).
After, against a local Nexus 3: [nexus-import-pick-1200-light.png](screenshots/nexus-import-pick-1200-light.png)
(every supported repository picked with the header checkbox),
[nexus-import-sync-1200-light.png](screenshots/nexus-import-sync-1200-light.png) (the first sync
and its log), [nexus-list-1200-light.png](screenshots/nexus-list-1200-light.png),
[nexus-import-390-light.png](screenshots/nexus-import-390-light.png).
