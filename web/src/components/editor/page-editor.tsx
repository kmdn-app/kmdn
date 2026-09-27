import { useEffect, useMemo, useState } from "react";
import { EditorContent, useEditor, useEditorState, type Editor } from "@tiptap/react";
import Collaboration from "@tiptap/extension-collaboration";
import CollaborationCaret from "@tiptap/extension-collaboration-caret";
import { Dropcursor, Gapcursor, Placeholder } from "@tiptap/extensions";
import { useTranslation } from "react-i18next";
import {
  Bold,
  Code,
  CodeXml,
  Heading1,
  Heading2,
  Heading3,
  Italic,
  Link2,
  List,
  ListChecks,
  ListOrdered,
  Minus,
  Pilcrow,
  Quote,
  Redo2,
  Strikethrough,
  Undo2,
} from "lucide-react";
import { CONTENT } from "@kmdn/doc-engine";
import { userColor } from "@/components/avatar";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { RoomProvider, RoomStatus } from "@/lib/realtime";
import { cn } from "@/lib/utils";
import { schemaExtensions } from "./schema";

export type EditorUser = { id: string; name: string };

export function useRoomStatus(provider: RoomProvider | null): RoomStatus | null {
  const [s, setS] = useState<RoomStatus | null>(provider?.status ?? null);
  useEffect(() => {
    if (!provider) return;
    return provider.onStatus(setS);
  }, [provider]);
  return s;
}

/**
 * The collaborative page editor. The document comes from the room (never
 * seeded here); editing is on only while the server says rw and the first
 * sync is done.
 */
export function PageEditor({
  provider,
  user,
  resolveImage,
  onEditor,
}: {
  provider: RoomProvider;
  user: EditorUser;
  resolveImage: (src: string) => string;
  onEditor?: (e: Editor | null) => void;
}) {
  const { t } = useTranslation();
  const status = useRoomStatus(provider);
  const extensions = useMemo(
    () => [
      ...schemaExtensions({ resolveImage }),
      Collaboration.configure({ document: provider.doc, field: CONTENT }),
      CollaborationCaret.configure({ provider, user: { name: user.name, color: userColor(user.id), id: user.id } }),
      Placeholder.configure({ placeholder: t("editor.placeholder") }),
      Dropcursor.configure({ width: 2, class: "drop-cursor" }),
      Gapcursor,
    ],
    // The editor is rebuilt only for a new room.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [provider],
  );
  const editor = useEditor(
    {
      extensions,
      editable: false,
      immediatelyRender: true,
      editorProps: { attributes: { class: "doc kmdn-editor", spellcheck: "true", "aria-label": t("editor.label") } },
    },
    [extensions],
  );
  const editable = status?.mode === "rw" && status.synced;
  useEffect(() => {
    editor?.setEditable(editable);
  }, [editor, editable]);
  useEffect(() => {
    onEditor?.(editor);
    return () => onEditor?.(null);
  }, [editor, onEditor]);
  return <EditorContent editor={editor} className={cn(!status?.synced && "opacity-60 transition-opacity")} />;
}

function Btn({ label, active, disabled, onClick, children }: { label: string; active?: boolean; disabled?: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={label}
          aria-pressed={active}
          disabled={disabled}
          onMouseDown={(e) => e.preventDefault()}
          onClick={onClick}
          className={cn(
            "grid size-7 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-40 [&_svg]:size-4",
            active && "bg-accent text-foreground",
          )}
        >
          {children}
        </button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

/** Formatting controls for the top bar. */
export function EditorToolbar({ editor }: { editor: Editor | null }) {
  const { t } = useTranslation();
  const s = useEditorState({
    editor,
    selector: ({ editor: e }) =>
      e
        ? {
            editable: e.isEditable,
            bold: e.isActive("bold"),
            italic: e.isActive("italic"),
            strike: e.isActive("strike"),
            code: e.isActive("code"),
            link: e.isActive("link"),
            p: e.isActive("paragraph"),
            h1: e.isActive("heading", { level: 1 }),
            h2: e.isActive("heading", { level: 2 }),
            h3: e.isActive("heading", { level: 3 }),
            ul: e.isActive("bulletList"),
            ol: e.isActive("orderedList"),
            quote: e.isActive("blockquote"),
            codeBlock: e.isActive("codeBlock"),
            canUndo: e.can().undo(),
            canRedo: e.can().redo(),
          }
        : null,
  });
  if (!editor || !s) return null;
  const off = !s.editable;
  const run = () => editor.chain().focus();
  const setLink = () => {
    const prev = editor.getAttributes("link").href as string | undefined;
    const href = window.prompt(t("editor.linkPrompt"), prev ?? "");
    if (href === null) return;
    if (href === "") run().unsetLink().run();
    else run().extendMarkRange("link").setLink({ href }).run();
  };
  const toggleTask = () => {
    const { $from } = editor.state.selection;
    for (let d = $from.depth; d > 0; d--) {
      const n = $from.node(d);
      if (n.type.name === "listItem") {
        editor.view.dispatch(editor.state.tr.setNodeAttribute($from.before(d), "checked", n.attrs.checked === null ? false : null));
        return;
      }
    }
    run().toggleBulletList().run();
  };
  return (
    <div role="toolbar" aria-label={t("editor.toolbar")} className="mx-auto flex w-max items-center gap-0.5">
      <Btn label={t("editor.undo")} disabled={off || !s.canUndo} onClick={() => run().undo().run()}>
        <Undo2 />
      </Btn>
      <Btn label={t("editor.redo")} disabled={off || !s.canRedo} onClick={() => run().redo().run()}>
        <Redo2 />
      </Btn>
      <span className="mx-1 h-4 w-px bg-border" />
      <Btn label={t("editor.paragraph")} active={s.p} disabled={off} onClick={() => run().setParagraph().run()}>
        <Pilcrow />
      </Btn>
      <Btn label={t("editor.heading", { level: 1 })} active={s.h1} disabled={off} onClick={() => run().toggleHeading({ level: 1 }).run()}>
        <Heading1 />
      </Btn>
      <Btn label={t("editor.heading", { level: 2 })} active={s.h2} disabled={off} onClick={() => run().toggleHeading({ level: 2 }).run()}>
        <Heading2 />
      </Btn>
      <Btn label={t("editor.heading", { level: 3 })} active={s.h3} disabled={off} onClick={() => run().toggleHeading({ level: 3 }).run()}>
        <Heading3 />
      </Btn>
      <span className="mx-1 h-4 w-px bg-border" />
      <Btn label={t("editor.bold")} active={s.bold} disabled={off} onClick={() => run().toggleBold().run()}>
        <Bold />
      </Btn>
      <Btn label={t("editor.italic")} active={s.italic} disabled={off} onClick={() => run().toggleItalic().run()}>
        <Italic />
      </Btn>
      <Btn label={t("editor.strike")} active={s.strike} disabled={off} onClick={() => run().toggleStrike().run()}>
        <Strikethrough />
      </Btn>
      <Btn label={t("editor.code")} active={s.code} disabled={off} onClick={() => run().toggleCode().run()}>
        <Code />
      </Btn>
      <Btn label={t("editor.link")} active={s.link} disabled={off} onClick={setLink}>
        <Link2 />
      </Btn>
      <span className="mx-1 h-4 w-px bg-border" />
      <Btn label={t("editor.bulletList")} active={s.ul} disabled={off} onClick={() => run().toggleBulletList().run()}>
        <List />
      </Btn>
      <Btn label={t("editor.orderedList")} active={s.ol} disabled={off} onClick={() => run().toggleOrderedList().run()}>
        <ListOrdered />
      </Btn>
      <Btn label={t("editor.taskList")} disabled={off} onClick={toggleTask}>
        <ListChecks />
      </Btn>
      <Btn label={t("editor.quote")} active={s.quote} disabled={off} onClick={() => run().toggleBlockquote().run()}>
        <Quote />
      </Btn>
      <Btn label={t("editor.codeBlock")} active={s.codeBlock} disabled={off} onClick={() => run().toggleCodeBlock().run()}>
        <CodeXml />
      </Btn>
      <Btn label={t("editor.rule")} disabled={off} onClick={() => run().setHorizontalRule().run()}>
        <Minus />
      </Btn>
    </div>
  );
}
