import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { ChevronDown, KeyRound, Loader2, Plus } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { CopyButton } from "@/components/copy-button";
import { Card, Panel } from "@/components/settings-layout";
import { Time } from "@/components/time";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap } from "@/lib/api";
import { useRepos } from "@/lib/repos";
import { cn } from "@/lib/utils";

type AgentKey = components["schemas"]["AgentKey"];

/** Calls per day as a small bar chart (14 days, oldest first). */
function Sparkline({ values, label }: { values: number[]; label: string }) {
  const top = Math.max(1, ...values);
  return (
    <svg viewBox={`0 0 ${values.length * 5} 20`} className="h-5 w-[70px] text-primary" role="img" aria-label={label}>
      {values.map((v, i) => {
        const h = v === 0 ? 1 : Math.max(2, (v / top) * 20);
        return <rect key={i} x={i * 5} y={20 - h} width={3.5} height={h} rx={0.75} className={v === 0 ? "fill-muted-foreground/25" : "fill-current"} />;
      })}
    </svg>
  );
}

function CreateDialog({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (v: boolean) => void; onCreated: () => void }) {
  const { t } = useTranslation();
  const { data: repos } = useRepos();
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [all, setAll] = useState(false);
  const [picked, setPicked] = useState<string[]>([]);
  const [days, setDays] = useState("90");
  const [shown, setShown] = useState<{ token: string; config: string; name: string } | null>(null);
  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/admin/agent-keys", { body: { name, description: desc, all_repos: all, repo_ids: all ? [] : picked, expires_in_days: Number(days) as 0 | 30 | 90 | 365 } })),
    onSuccess: (d) => {
      setShown({ token: d.token, config: d.config, name: d.key.name });
      onCreated();
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const close = () => {
    onOpenChange(false);
    setShown(null);
    setName("");
    setDesc("");
    setAll(false);
    setPicked([]);
    setDays("90");
  };
  return (
    <Dialog open={open} onOpenChange={(v) => !v && close()}>
      <DialogContent className="sm:max-w-lg">
        {shown ? (
          <>
            <DialogHeader>
              <DialogTitle>{t("agentKeys.createdTitle", { name: shown.name })}</DialogTitle>
              <DialogDescription>{t("agentKeys.onceWarning")}</DialogDescription>
            </DialogHeader>
            <div className="grid gap-3">
              <div className="grid gap-1.5">
                <Label>{t("agentKeys.token")}</Label>
                <div className="flex gap-2">
                  <Input readOnly value={shown.token} className="font-mono text-[12px]" onFocus={(e) => e.currentTarget.select()} />
                  <CopyButton text={shown.token} label={t("agentKeys.copy")} />
                </div>
              </div>
              <div className="grid gap-1.5">
                <div className="flex items-center justify-between">
                  <Label>{t("agentKeys.config")}</Label>
                  <CopyButton text={shown.config} label={t("agentKeys.copyConfig")} />
                </div>
                <pre className="max-h-56 overflow-auto rounded-lg bg-muted p-3 font-mono text-[11.5px] leading-relaxed">{shown.config}</pre>
              </div>
            </div>
            <DialogFooter>
              <Button onClick={close}>{t("agentKeys.done")}</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t("agentKeys.create")}</DialogTitle>
              <DialogDescription>{t("agentKeys.createDesc")}</DialogDescription>
            </DialogHeader>
            <form
              id="create-agent-key"
              className="grid gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                create.mutate();
              }}
            >
              <div className="grid gap-1.5">
                <Label htmlFor="ak-name">{t("agentKeys.name")}</Label>
                <Input id="ak-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t("agentKeys.namePlaceholder")} maxLength={80} autoFocus />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="ak-desc">{t("agentKeys.description")}</Label>
                <Textarea id="ak-desc" rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} maxLength={500} />
              </div>
              <div className="grid gap-2">
                <Label>{t("agentKeys.scope")}</Label>
                <label className="flex items-center gap-2 text-[13.5px]">
                  <Checkbox checked={all} onCheckedChange={(v) => setAll(v === true)} />
                  {t("agentKeys.allRepos")}
                </label>
                {!all && (
                  <div className="grid max-h-44 gap-1.5 overflow-auto rounded-lg border p-2.5">
                    {(repos ?? []).map((r) => (
                      <label key={r.id} className="flex items-center gap-2 text-[13.5px]">
                        <Checkbox checked={picked.includes(r.id)} onCheckedChange={(v) => setPicked((p) => (v === true ? [...p, r.id] : p.filter((x) => x !== r.id)))} />
                        <span className="truncate">{r.slug}</span>
                      </label>
                    ))}
                  </div>
                )}
              </div>
              <div className="grid gap-1.5">
                <Label>{t("agentKeys.expires")}</Label>
                <Select value={days} onValueChange={setDays}>
                  <SelectTrigger className="w-48">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {["30", "90", "365", "0"].map((d) => (
                      <SelectItem key={d} value={d}>
                        {d === "0" ? t("agentKeys.never") : t("agentKeys.days", { count: Number(d) })}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </form>
            <DialogFooter>
              <Button variant="ghost" onClick={close}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" form="create-agent-key" disabled={create.isPending || !name.trim() || (!all && picked.length === 0)}>
                {create.isPending && <Loader2 className="animate-spin" />}
                {t("agentKeys.create")}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

function Calls({ id }: { id: string }) {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ["agent-key-calls", id], queryFn: async () => (await unwrap(api.GET("/admin/agent-keys/{id}/calls", { params: { path: { id } } }))).items });
  if (!q.data) return <Loader2 className="m-3 size-4 animate-spin text-muted-foreground" />;
  if (q.data.length === 0) return <p className="px-4 py-3 text-[13px] text-muted-foreground">{t("agentKeys.noCalls")}</p>;
  return (
    <ul className="grid gap-1 px-4 py-3 text-[12.5px]">
      {q.data.map((e) => {
        const d = (e.data ?? {}) as Record<string, unknown>;
        const what = [d.repo, d.path, d.query && `“${String(d.query)}”`, d.uri].filter(Boolean).join(" · ");
        return (
          <li key={e.id} className="flex min-w-0 items-baseline gap-2">
            <span className="shrink-0 text-muted-foreground tabular-nums">
              <Time iso={e.at} />
            </span>
            <code className="shrink-0 rounded bg-muted px-1 font-mono text-[11.5px]">{e.action.replace(/^mcp\./, "")}</code>
            <span className={cn("truncate", d.error || d.tool_error ? "text-destructive" : "text-muted-foreground")}>{what}</span>
          </li>
        );
      })}
    </ul>
  );
}

/** Admin → Agent keys (docs/specs/12-mcp.md#admin-console). */
export function AgentKeysPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data: repos } = useRepos();
  const q = useQuery({ queryKey: ["agent-keys"], queryFn: () => unwrap(api.GET("/admin/agent-keys")) });
  const [creating, setCreating] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<AgentKey | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: ["agent-keys"] });
  const revoke = useMutation({
    mutationFn: (k: AgentKey) => unwrap(api.POST("/admin/agent-keys/{id}/revoke", { params: { path: { id: k.id } } })),
    onSuccess: () => (setRevoking(null), refresh()),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const repoName = (id: string) => repos?.find((r) => r.id === id)?.slug ?? id;
  return (
    <Panel
      title={t("agentKeys.title")}
      desc={
        <>
          {t("agentKeys.desc")} {q.data && <code className="rounded bg-muted px-1 font-mono text-[12px]">{q.data.endpoint}</code>}
        </>
      }
      actions={
        <Button size="sm" onClick={() => setCreating(true)}>
          <Plus />
          {t("agentKeys.create")}
        </Button>
      }
    >
      <CreateDialog open={creating} onOpenChange={setCreating} onCreated={refresh} />
      <Dialog open={!!revoking} onOpenChange={(v) => !v && setRevoking(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("agentKeys.revokeTitle", { name: revoking?.name })}</DialogTitle>
            <DialogDescription>{t("agentKeys.revokeDesc")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRevoking(null)}>
              {t("common.cancel")}
            </Button>
            <Button variant="destructive" disabled={revoke.isPending} onClick={() => revoking && revoke.mutate(revoking)}>
              {revoke.isPending && <Loader2 className="animate-spin" />}
              {t("agentKeys.revoke")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {q.data && q.data.items.length === 0 && (
        <Card>
          <div className="flex items-start gap-3 text-[13.5px]">
            <KeyRound className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            <p className="text-muted-foreground">{t("agentKeys.none")}</p>
          </div>
        </Card>
      )}
      {q.data && q.data.items.length > 0 && (
        <div className="overflow-hidden rounded-xl border">
          {q.data.items.map((k) => (
            <div key={k.id} className="border-b last:border-b-0">
              <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{k.name}</span>
                    <span
                      className={cn(
                        "rounded-full px-2 py-0.5 text-[11.5px] font-medium",
                        k.status === "active" ? "bg-success/10 text-success" : "bg-muted text-muted-foreground",
                      )}
                    >
                      {t(`agentKeys.status.${k.status}`)}
                    </span>
                    {k.all_repos ? (
                      <span className="rounded-md border px-1.5 py-0.5 text-[11.5px]">{t("agentKeys.allRepos")}</span>
                    ) : (
                      k.repo_ids.map((id) => (
                        <span key={id} className="rounded-md border px-1.5 py-0.5 text-[11.5px]">
                          {repoName(id)}
                        </span>
                      ))
                    )}
                  </div>
                  <div className="mt-1 flex flex-wrap gap-x-3 text-[12px] text-muted-foreground">
                    <span>
                      {t("agentKeys.createdBy", { name: k.created_by_name || "—" })} <Time iso={k.created_at} />
                    </span>
                    <span>{k.expires_at ? <>{t("agentKeys.expiresOn")} <Time iso={k.expires_at} /></> : t("agentKeys.never")}</span>
                    <span>
                      {k.last_used_at ? (
                        <>
                          {t("agentKeys.lastUsed")} <Time iso={k.last_used_at} />
                          {k.last_used_ip && ` · ${k.last_used_ip}`}
                        </>
                      ) : (
                        t("agentKeys.neverUsed")
                      )}
                    </span>
                  </div>
                </div>
                <Sparkline values={k.usage} label={t("agentKeys.usage", { count: k.usage.reduce((a, b) => a + b, 0) })} />
                <Button size="sm" variant="ghost" onClick={() => setOpen(open === k.id ? null : k.id)} aria-expanded={open === k.id}>
                  {t("agentKeys.calls")}
                  <ChevronDown className={cn("transition-transform", open === k.id && "rotate-180")} />
                </Button>
                {k.status === "active" && (
                  <Button size="sm" variant="outline" onClick={() => setRevoking(k)}>
                    {t("agentKeys.revoke")}
                  </Button>
                )}
              </div>
              {open === k.id && (
                <div className="border-t bg-muted/30">
                  <Calls id={k.id} />
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </Panel>
  );
}
