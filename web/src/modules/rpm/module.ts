// The UI of the rpm module: its part of the details of a package file under Packages/ (file_details
// of the format's descriptor).

import type { ModuleUI } from "../types";

export const rpm: ModuleUI = {
  id: "rpm",
  formatParts: { rpm: { "rpm-package": () => import("./PackagePart.svelte") } },
};