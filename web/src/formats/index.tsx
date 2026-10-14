import { useState, type ComponentType } from "react";
import type { RepoConfig } from "../api";
import Debian from "./debian";

// A config editor edits one repo config object. It calls onChange with the new object, or with
// null while the input is not a valid config.
export type EditorProps = { value: RepoConfig; onChange: (v: RepoConfig | null) => void };

// Formats without an entry here use the JSON editor.
const editors: Record<string, ComponentType<EditorProps>> = {
  debian: Debian,
};

function JsonEditor({ value, onChange }: EditorProps) {
  const [text, setText] = useState(() => JSON.stringify(value, null, 2));
  const [err, setErr] = useState("");
  const edit = (t: string) => {
    setText(t);
    try {
      const v = JSON.parse(t);
      if (v === null || typeof v !== "object" || Array.isArray(v)) throw new Error("config must be a JSON object");
      setErr(""); onChange(v);
    } catch (e) { setErr((e as Error).message); onChange(null); }
  };
  return (
    <div className="grow">
      <textarea className="mono" style={{ width: "100%" }} rows={Math.min(12, Math.max(3, text.split("\n").length))} spellCheck={false} value={text} onChange={(e) => edit(e.target.value)} />
      {err && <p className="error">{err}</p>}
    </div>
  );
}

// ConfigEditor renders the editor of the format. Give it a key that changes with the format or
// the repo, so that its state resets.
export function ConfigEditor({ format, ...p }: EditorProps & { format: string }) {
  const E = editors[format] ?? JsonEditor;
  return <E {...p} />;
}
