/** One file of an upload before it is sent: where the server stores it, or what is wrong with it. */
export type FileRow = { file: File; target?: string; error?: string };

/** What a format's upload fields report to the Upload dialog. */
export type UploadPlan = {
  rows: FileRow[];
  /** The form fields to send (empty values are left out). */
  fields: Record<string, string>;
  /** Why the upload cannot go yet; "" when it can. */
  problem: string;
};
