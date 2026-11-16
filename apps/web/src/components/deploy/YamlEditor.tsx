import Editor, { loader } from "@monaco-editor/react";
import * as monaco from "monaco-editor/editor/editor.api";
import "monaco-editor/languages/definitions/yaml/register";
import EditorWorker from "monaco-editor/editor/editor.worker?worker";
import { useTheme } from "@/components/theme";

// Bundle Monaco locally (no CDN): the platform often runs in isolated networks.
self.MonacoEnvironment = { getWorker: () => new EditorWorker() };
loader.config({ monaco });

/** Monaco YAML editor that follows the light/dark theme. */
export default function YamlEditor({
  value,
  onChange,
  height = 480,
  readOnly = false,
}: {
  value: string;
  onChange?: (v: string) => void;
  height?: number | string;
  readOnly?: boolean;
}) {
  const { resolved } = useTheme();
  return (
    <div className="overflow-hidden rounded-md border">
      <Editor
        height={height}
        language="yaml"
        theme={resolved === "dark" ? "vs-dark" : "vs"}
        value={value}
        onChange={(v) => onChange?.(v ?? "")}
        options={{
          readOnly,
          minimap: { enabled: false },
          fontSize: 13,
          tabSize: 2,
          insertSpaces: true,
          scrollBeyondLastLine: false,
          automaticLayout: true,
          wordWrap: "on",
          renderWhitespace: "boundary",
        }}
      />
    </div>
  );
}
