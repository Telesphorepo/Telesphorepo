// The UI of the backup module: the backup settings, Back up now and the list of backups.

import type { ModuleUI } from "../types";
import ArchiveIcon from "@lucide/svelte/icons/archive";

export const backup: ModuleUI = {
  id: "backup",
  pages: [
    {
      path: "/settings/backups", layout: "settings", access: "backup.manage", title: "Backups",
      page: () => import("../../pages/server/Backups.svelte"),
      nav: { group: "server", label: "Backups", icon: ArchiveIcon, order: 32 },
    },
  ],
};
