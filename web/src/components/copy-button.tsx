import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";

/** Copies text to the clipboard, with a check mark for a moment after. */
export function CopyButton({ text, label }: { text: string; label: string }) {
  const { t } = useTranslation();
  const [done, setDone] = useState(false);
  return (
    <Button
      type="button"
      size="sm"
      variant="outline"
      onClick={() =>
        void navigator.clipboard.writeText(text).then(
          () => (setDone(true), setTimeout(() => setDone(false), 1500)),
          () => toast.error(t("errors.generic")),
        )
      }
    >
      {done ? <Check /> : <Copy />}
      {label}
    </Button>
  );
}
