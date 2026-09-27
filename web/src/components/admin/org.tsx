import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { CheckCircle2, Clock, Loader2, Plus, Trash2 } from "lucide-react";
import type { components } from "@kmdn/api-client";
import { Card, Panel, Row } from "@/components/settings-layout";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api, errorMessage, unwrap } from "@/lib/api";
import { currentOrg, orgsInfoQuery, useCurrentOrg } from "@/lib/orgs";

type Org = components["schemas"]["Org"];

/** Org console → Organization: name, address, AI switch, email domains. */
export function OrganizationPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const org = useCurrentOrg();
  const [name, setName] = useState<string | null>(null);
  const [slug, setSlug] = useState<string | null>(null);
  const [domain, setDomain] = useState("");
  const settings = useQuery({ queryKey: ["org-settings", currentOrg()], queryFn: () => unwrap(api.GET("/orgs/{org}/admin/settings", { params: { path: { org: currentOrg() } } })) });
  const domains = useQuery({ queryKey: ["org-domains", currentOrg()], queryFn: async () => (await unwrap(api.GET("/orgs/{org}/admin/domains", { params: { path: { org: currentOrg() } } }))).items });
  const save = useMutation({
    mutationFn: () =>
      unwrap(api.PATCH("/orgs/{org}", { params: { path: { org: currentOrg() } }, body: { ...(name !== null ? { name } : {}), ...(slug !== null ? { slug } : {}) } })),
    onSuccess: async (o) => {
      toast.success(t("orgs.saved"));
      setName(null);
      setSlug(null);
      await qc.invalidateQueries({ queryKey: ["orgs"] });
      if (o.slug !== currentOrg()) await navigate({ to: "/$org/admin", params: { org: o.slug }, search: { section: "organization" } });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const setAssistant = useMutation({
    mutationFn: (on: boolean) => unwrap(api.PATCH("/orgs/{org}/admin/settings", { params: { path: { org: currentOrg() } }, body: { assistant: on } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["org-settings"] });
      void qc.invalidateQueries({ queryKey: ["assistant-status"] });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const addDomain = useMutation({
    mutationFn: () => unwrap(api.POST("/orgs/{org}/admin/domains", { params: { path: { org: currentOrg() } }, body: { domain } })),
    onSuccess: () => (setDomain(""), void qc.invalidateQueries({ queryKey: ["org-domains"] })),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const removeDomain = useMutation({
    mutationFn: (d: string) => unwrap(api.DELETE("/orgs/{org}/admin/domains/{domain}", { params: { path: { org: currentOrg(), domain: d } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["org-domains"] }),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  if (!org) return null;
  const owner = org.role === "owner";
  const assistantLocked = settings.data?.locked.includes("assistant");
  return (
    <Panel title={t("orgs.organization")} desc={t("orgs.organizationDesc")}>
      <Card
        footer={
          <Button size="sm" disabled={(name === null && slug === null) || save.isPending} onClick={() => save.mutate()}>
            {save.isPending && <Loader2 className="animate-spin" />}
            {t("orgs.save")}
          </Button>
        }
      >
        <div className="grid gap-1.5">
          <Label htmlFor="org-name">{t("orgs.name")}</Label>
          <Input id="org-name" value={name ?? org.name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="org-slug">{t("orgs.slug")}</Label>
          <Input id="org-slug" value={slug ?? org.slug} disabled={!owner} onChange={(e) => setSlug(e.target.value.toLowerCase())} />
          <p className="text-xs text-muted-foreground">{t("orgs.slugHint", { url: `${window.location.origin}/${slug ?? org.slug}` })}</p>
        </div>
      </Card>
      <Card>
        <Row title={t("orgs.assistant")} desc={assistantLocked ? t("orgs.managed") : t("orgs.assistantHint")}>
          <Switch
            aria-label={t("orgs.assistant")}
            checked={settings.data?.settings.assistant ?? true}
            disabled={assistantLocked || setAssistant.isPending}
            onCheckedChange={(v) => setAssistant.mutate(v)}
          />
        </Row>
      </Card>
      <Card>
        <div>
          <h3 className="font-medium">{t("orgs.domains")}</h3>
          <p className="text-[0.8125rem] text-muted-foreground">{t("orgs.domainsHint")}</p>
        </div>
        {(domains.data ?? []).map((d) => (
          <div key={d.domain} className="flex items-center gap-2 text-[0.84375rem]">
            <span className="font-medium">{d.domain}</span>
            {d.verified_at ? (
              <Badge variant="secondary">
                <CheckCircle2 />
                {t("orgs.verified")}
              </Badge>
            ) : (
              <Badge variant="outline">
                <Clock />
                {t("orgs.unverified")}
              </Badge>
            )}
            <Button variant="ghost" size="icon" className="ml-auto size-8" aria-label={t("orgs.removeDomain")} onClick={() => removeDomain.mutate(d.domain)}>
              <Trash2 />
            </Button>
          </div>
        ))}
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (domain.trim()) addDomain.mutate();
          }}
        >
          <Input value={domain} placeholder={t("orgs.domainPlaceholder")} aria-label={t("orgs.addDomain")} onChange={(e) => setDomain(e.target.value)} />
          <Button type="submit" variant="outline" disabled={!domain.trim() || addDomain.isPending}>
            <Plus />
            {t("orgs.addDomain")}
          </Button>
        </form>
      </Card>
    </Panel>
  );
}

/** Instance console → Organizations (multi mode): every org, suspend or resume, create. */
export function OrgsPanel() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  const info = useQuery(orgsInfoQuery);
  const all = useQuery({ queryKey: ["admin-orgs"], queryFn: async () => (await unwrap(api.GET("/admin/orgs"))).items });
  const setStatus = useMutation({
    mutationFn: (o: Org) =>
      unwrap(api.PATCH("/admin/orgs/{org}", { params: { path: { org: o.slug } }, body: { status: o.status === "active" ? "suspended" : "active" } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["admin-orgs"] }),
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/orgs", { body: { name: newName } })),
    onSuccess: async (o) => {
      toast.success(t("orgs.created"));
      setCreating(false);
      setNewName("");
      await qc.invalidateQueries({ queryKey: ["orgs"] });
      await qc.invalidateQueries({ queryKey: ["admin-orgs"] });
      await navigate({ to: "/$org/admin", params: { org: o.slug }, search: { section: "organization" } });
    },
    onError: (e) => toast.error(errorMessage(e, t("errors.generic"))),
  });
  return (
    <Panel
      title={t("orgs.all")}
      desc={t("orgs.allDesc")}
      actions={
        info.data?.can_create && (
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus />
            {t("orgs.create")}
          </Button>
        )
      }
    >
      <div className="overflow-x-auto rounded-xl border">
        <table className="w-full text-[0.84375rem]">
          <tbody>
            {(all.data ?? []).map((o) => (
              <tr key={o.id} className="border-b last:border-0">
                <td className="px-3 py-2.5">
                  <div className="font-medium">{o.name}</div>
                  <div className="text-xs text-muted-foreground">/{o.slug}</div>
                </td>
                <td className="px-3 py-2.5">
                  <Badge variant={o.status === "active" ? "secondary" : "outline"}>{t(`orgs.status_${o.status === "active" ? "active" : "suspended"}`)}</Badge>
                </td>
                <td className="px-3 py-2.5 text-right">
                  <Button variant="ghost" size="sm" onClick={() => setStatus.mutate(o)}>
                    {o.status === "active" ? t("orgs.suspend") : t("orgs.resume")}
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("orgs.create")}</DialogTitle>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="new-org">{t("orgs.createName")}</Label>
            <Input id="new-org" value={newName} onChange={(e) => setNewName(e.target.value)} />
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCreating(false)}>
              {t("common.cancel")}
            </Button>
            <Button disabled={!newName.trim() || create.isPending} onClick={() => create.mutate()}>
              {create.isPending && <Loader2 className="animate-spin" />}
              {t("orgs.create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Panel>
  );
}
