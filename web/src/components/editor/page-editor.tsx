import { useEffect, useMemo, useRef, useState } from "react";
import type { EditorView } from "@tiptap/pm/view";
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
  ImagePlus,
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
import { toast } from "sonner";
import { schemaExtensions } from "./schema";
import { EditorDocContext, nodeViewExtensions } from "./node-views";
import type { DocContext } from "@/components/doc/doc-view";
import type { AnyExtension } from "@tiptap/core";

/** Replaces schema nodes by their versions with node views (same name, same schema). */
function withViews(base: AnyExtension[], views: AnyExtension[]): AnyExtension[] {
  const byName = new Map(views.map((v) => [v.name, v]));
  return base.map((e) => byName.get(e.name) ?? e);
}

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
/** Uploads an image and returns the src to store in the page (relative path). */
export type ImageUploader = (file: File) => Promise<{ src: string; alt: string }>;

const IMAGE_TYPES = /^image\/(png|jpe?g|gif|webp|avif|svg\+xml)$/;

export function PageEditor({
  provider,
  user,
  resolveImage,
  upload,
  onEditor,
  docCtx,
}: {
  provider: RoomProvider;
  user: EditorUser;
  resolveImage: (src: string) => string;
  upload?: ImageUploader;
  onEditor?: (e: Editor | null) => void;
  /** Links and images in previews inside the editor (raw blocks, alerts). */
  docCtx: DocContext;
}) {
  const { t } = useTranslation();
  const status = useRoomStatus(provider);
  const uploadRef = useRef(upload);
  useEffect(() => {
    uploadRef.current = upload;
  }, [upload]);
  const extensions = useMemo(
    () => [
      ...withViews(schemaExtensions({ resolveImage }), nodeViewExtensions),
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
      editorProps: {
        attributes: { class: "doc kmdn-editor", spellcheck: "true", "aria-label": t("editor.label") },
        // Images pasted or dropped are uploaded into the revision, then inserted.
        handlePaste: (view, e) => {
          const files = [...(e.clipboardData?.files ?? [])].filter((f) => IMAGE_TYPES.test(f.type));
          if (!files.length || !uploadRef.current || !view.editable) return false;
          e.preventDefault();
          void insertImages(view, view.state.selection.from, files);
          return true;
        },
        handleDrop: (view, e, _slice, moved) => {
          const files = [...(e.dataTransfer?.files ?? [])].filter((f) => IMAGE_TYPES.test(f.type));
          if (moved || !files.length || !uploadRef.current || !view.editable) return false;
          e.preventDefault();
          const at = view.posAtCoords({ left: e.clientX, top: e.clientY })?.pos ?? view.state.selection.from;
          void insertImages(view, at, files);
          return true;
        },
      },
    },
    [extensions],
  );
  // The paste/drop handlers are created once; they read the uploader through a ref.
  async function insertImages(view: EditorView, at: number, files: File[]) {
    for (const f of files) {
      try {
        const { src, alt } = await uploadRef.current!(f);
        const node = view.state.schema.nodes.image!.create({ src, alt });
        const pos = Math.min(at, view.state.doc.content.size);
        view.dispatch(view.state.tr.insert(pos, node));
      } catch (err) {
        toast.error(err instanceof Error ? err.message : t("errors.generic"));
      }
    }
  }
  const editable = status?.mode === "rw" && status.synced;
  useEffect(() => {
    editor?.setEditable(editable);
  }, [editor, editable]);
  useEffect(() => {
    onEditor?.(editor);
    return () => onEditor?.(null);
  }, [editor, onEditor]);
  return (
    <EditorDocContext.Provider value={docCtx}>
      <EditorContent editor={editor} className={cn(!status?.synced && "opacity-60 transition-opacity")} />
    </EditorDocContext.Provider>
  );
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
export function EditorToolbar({ editor, upload }: { editor: Editor | null; upload?: ImageUploader }) {
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
  const pickImage = () => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = "image/png,image/jpeg,image/gif,image/webp,image/avif,image/svg+xml";
    input.onchange = async () => {
      const f = input.files?.[0];
      if (!f || !upload) return;
      try {
        const { src, alt } = await upload(f);
        run().insertContent({ type: "image", attrs: { src, alt } }).run();
      } catch (err) {
        toast.error(err instanceof Error ? err.message : t("errors.generic"));
      }
    };
    input.click();
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
      {upload && (
        <Btn label={t("editor.image")} disabled={off} onClick={pickImage}>
          <ImagePlus />
        </Btn>
      )}
    </div>
  );
}
