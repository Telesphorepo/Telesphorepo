// The UI of the arch module: its part of the details of a package file in the pool (file_details
// of the format's descriptor).

import type { ModuleUI } from "../types";

export const arch: ModuleUI = {
  id: "arch",
  formatParts: { arch: { "arch-package": () => import("./PackagePart.svelte") } },
};
