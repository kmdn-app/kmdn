import { Outlet, createFileRoute, redirect } from "@tanstack/react-router";
import { meQuery, setupStatusQuery } from "@/lib/api";

/** Signed-in area. Sends people to setup or sign-in when needed. */
export const Route = createFileRoute("/_app")({
  beforeLoad: async ({ context, location }) => {
    const [status, me] = await Promise.all([
      context.queryClient.ensureQueryData(setupStatusQuery),
      context.queryClient.ensureQueryData(meQuery),
    ]);
    if (!status.admin_exists) throw redirect({ to: "/setup" });
    if (!me) throw redirect({ to: "/signin", search: { redirect: location.href } });
    if (status.needed && me.is_instance_admin) throw redirect({ to: "/setup" });
    return { me };
  },
  component: Outlet,
});
