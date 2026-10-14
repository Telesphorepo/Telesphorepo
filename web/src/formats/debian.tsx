import { useState } from "react";
import type { EditorProps } from "./index";

const archs = (v: unknown) => (Array.isArray(v) ? v.join(", ") : "");

export default function Debian({ value, onChange }: EditorProps) {
  const [text, setText] = useState(archs(value.architectures));
  return (
    <input className="grow" placeholder="architectures (comma separated)" value={text}
      onChange={(e) => {
        setText(e.target.value);
        onChange({ ...value, architectures: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) });
      }} />
  );
}
