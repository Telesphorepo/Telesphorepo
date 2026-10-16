// The UI of the signing module: the signing keys, the Signing tab of a repository's settings and
// the signing key of a new repository (the repo field signing_key).

import type { ModuleUI } from "../types";
import FileKeyIcon from "@lucide/svelte/icons/file-key";

export const signing: ModuleUI = {
  id: "signing",
  pages: [
    {
      path: "/settings/signing-keys", layout: "settings", access: "signing.manage", title: "Signing keys",
      page: () => import("../../pages/server/SigningKeys.svelte"),
      nav: { group: "server", label: "Signing keys", icon: FileKeyIcon, order: 20 },
    },
    {
      path: "/settings/signing-keys/new", layout: "settings", access: "signing.manage", title: "New signing key",
      page: () => import("../../pages/server/NewSigningKey.svelte"),
    },
  ],
  repoTabs: [{ id: "signing", label: "Signing", access: "repo.settings", icon: FileKeyIcon, component: () => import("./SigningTab.svelte") }],
  repoFields: [{ field: "signing_key", label: "Signing key", component: () => import("./SigningKeyField.svelte") }],
};
