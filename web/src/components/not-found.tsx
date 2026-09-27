import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";

export function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
      <h1 className="text-xl font-semibold tracking-tight">{t("errors.notFoundTitle")}</h1>
      <p className="max-w-md text-muted-foreground">{t("errors.notFound")}</p>
      <Button asChild variant="outline" className="mt-2">
        <Link to="/">{t("errors.backHome")}</Link>
      </Button>
    </div>
  );
}
