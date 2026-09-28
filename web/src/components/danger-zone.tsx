import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, Trash2 } from "lucide-react";
import { Card, Row } from "@/components/settings-layout";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ProblemMessage } from "@/components/problem-message";

/** A destructive action behind a dialog that asks to type `expected` first. */
export function DangerZone({
  title,
  desc,
  action,
  confirmLabel,
  expected,
  onConfirm,
  pending,
  error,
}: {
  title: string;
  desc: string;
  action: string;
  confirmLabel: string;
  expected: string;
  onConfirm: (typed: string) => void;
  pending: boolean;
  error: unknown;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  return (
    <Card>
      <Row title={title} desc={desc}>
        <Button variant="destructive" size="sm" onClick={() => setOpen(true)}>
          <Trash2 />
          {action}
        </Button>
      </Row>
      <Dialog open={open} onOpenChange={(v) => (setOpen(v), setTyped(""))}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>{desc}</DialogDescription>
          </DialogHeader>
          <form
            className="grid gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (typed.trim().toLowerCase() === expected.toLowerCase()) onConfirm(typed.trim());
            }}
          >
            <div className="grid gap-1.5">
              <Label htmlFor="danger-confirm">{confirmLabel}</Label>
              <Input id="danger-confirm" autoComplete="off" value={typed} placeholder={expected} onChange={(e) => setTyped(e.target.value)} />
            </div>
            {!!error && <ProblemMessage error={error} />}
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" variant="destructive" disabled={pending || typed.trim().toLowerCase() !== expected.toLowerCase()}>
                {pending && <Loader2 className="animate-spin" />}
                {action}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
