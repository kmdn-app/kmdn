import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";

type VersionInfo = { version: string; commit: string };

export const Route = createFileRoute("/")({
  component: Home,
});

function Home() {
  const { data } = useQuery({
    queryKey: ["version"],
    queryFn: async (): Promise<VersionInfo> => {
      const r = await fetch("/version");
      if (!r.ok) throw new Error(`version: ${r.status}`);
      return r.json();
    },
  });
  return (
    <main className="mx-auto flex h-full max-w-xl flex-col justify-center gap-3 px-4">
      <div className="grid size-11 place-items-center rounded-[11px] bg-primary font-mono text-xl font-semibold text-primary-foreground">
        k
      </div>
      <h1 className="text-2xl font-semibold tracking-tight">kmdn</h1>
      <p className="text-muted-foreground">Collaborative markdown editing with a git backend.</p>
      <p className="font-mono text-xs text-muted-foreground">{data ? `v${data.version} · ${data.commit}` : " "}</p>
    </main>
  );
}
