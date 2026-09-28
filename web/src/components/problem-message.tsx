import { useTranslation } from "react-i18next";
import { ApiError } from "@kmdn/api-client";
import { errorMessage } from "@/lib/api";

/** An error under a form, with the way out when the server gives one (an upgrade link for a limit). */
export function ProblemMessage({ error, className }: { error: unknown; className?: string }) {
  const { t } = useTranslation();
  const upgrade = error instanceof ApiError && error.problem.code === "limit_reached" ? error.problem.params?.upgrade_url : undefined;
  return (
    <p role="alert" className={className ?? "text-[0.8125rem] text-destructive"}>
      {errorMessage(error, t("errors.generic"))}
      {typeof upgrade === "string" && upgrade && (
        <>
          {" "}
          <a className="font-medium underline underline-offset-2" href={upgrade}>
            {t("orgs.upgrade")}
          </a>
        </>
      )}
    </p>
  );
}
