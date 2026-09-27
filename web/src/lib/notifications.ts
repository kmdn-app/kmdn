import { useEffect } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "@kmdn/api-client";
import { api, unwrap } from "@/lib/api";
import { realtime } from "@/lib/realtime";

export type Notification = components["schemas"]["Notification"];

/** The inbox, kept live by the user's event scope. */
export function useNotifications() {
  const qc = useQueryClient();
  useEffect(
    () =>
      realtime.follow("user", (ev) => {
        if (ev.type === "notification") void qc.invalidateQueries({ queryKey: ["notifications"] });
      }),
    [qc],
  );
  return useQuery({ queryKey: ["notifications"], queryFn: () => unwrap(api.GET("/notifications")), staleTime: 30_000 });
}

export function useMarkRead() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[] | "all") => unwrap(api.POST("/notifications/read", { body: ids === "all" ? { all: true } : { ids } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["notifications"] }),
  });
}

/** Where a notification leads. */
export function notificationHref(n: Notification): string | null {
  const d = n.data;
  if (!d.owner || !d.repo) return null;
  if (d.number && (n.kind === "mention" || n.kind === "reply") && d.path) return `/${d.owner}/${d.repo}/${d.path}?revision=${d.number}`;
  if (d.number) return `/${d.owner}/${d.repo}/revisions/${d.number}`;
  if (d.path) return `/${d.owner}/${d.repo}/${d.path}`;
  return null;
}

// --- Browser push -----------------------------------------------------------

function base64ToBytes(s: string): Uint8Array<ArrayBuffer> {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

export const pushSupported = () => typeof window !== "undefined" && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;

async function registration() {
  return navigator.serviceWorker.register("/sw.js", { scope: "/" });
}

/** This browser's push subscription, if any. */
export async function currentSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null;
  const reg = await navigator.serviceWorker.getRegistration("/");
  return (await reg?.pushManager.getSubscription()) ?? null;
}

/** Asks permission and subscribes this browser. */
export async function enablePush(): Promise<boolean> {
  if (!pushSupported()) return false;
  if ((await Notification.requestPermission()) !== "granted") return false;
  const { public_key } = await unwrap(api.GET("/push/key"));
  const reg = await registration();
  await navigator.serviceWorker.ready;
  const sub = (await reg.pushManager.getSubscription()) ?? (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: base64ToBytes(public_key) }));
  const json = sub.toJSON() as { endpoint: string; keys?: { p256dh?: string; auth?: string } };
  await unwrap(api.POST("/me/push-subscriptions", { body: { endpoint: json.endpoint, keys: { p256dh: json.keys?.p256dh ?? "", auth: json.keys?.auth ?? "" } } }));
  return true;
}

export async function disablePush(): Promise<void> {
  const sub = await currentSubscription();
  if (!sub) return;
  await unwrap(api.POST("/me/push-subscriptions/remove", { body: { endpoint: sub.endpoint } }));
  await sub.unsubscribe();
}
