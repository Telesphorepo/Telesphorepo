// The UI of the debian module: its part of the details of a .deb in the pool (file_details of the
// format's descriptor).

import type { ModuleUI } from "../types";

export const debian: ModuleUI = {
  id: "debian",
  formatParts: { debian: { "deb-package": () => import("./PackagePart.svelte") } },
};
