import { Button, Input, Label, TextField } from "@heroui/react";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { apiFetch } from "@/api/client";
import { userMessage } from "@/api/errors";
import { useAppLocale } from "@/lib/locale";
import { AppDialog } from "./dialogs/app-dialog";

type ImportResult = {
  fileId: string;
  name: string;
  size: number;
  mimeType: string;
};

export function ImportTelegramLinkDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [link, setLink] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const { t } = useAppLocale();
  const queryClient = useQueryClient();

  const close = () => {
    if (submitting) return;
    onOpenChange(false);
  };

  const submit = async () => {
    const url = link.trim();
    if (!url) {
      toast.error(t("imports.linkRequired"));
      return;
    }
    setSubmitting(true);
    try {
      const response = await apiFetch("/v1/import/telegram-link", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ url }),
      });
      const result = (await response.json()) as ImportResult;
      toast.success(t("imports.created", { name: result.name }));
      await queryClient.invalidateQueries();
      setLink("");
      onOpenChange(false);
    } catch (error) {
      toast.error(userMessage(error));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <AppDialog
      open={open}
      onOpenChange={(next) => (next ? onOpenChange(true) : close())}
      isDismissable={!submitting}
      title={t("imports.title")}
      description={t("imports.desc")}
      footer={
        <>
          <Button variant="secondary" isDisabled={submitting} onPress={close}>
            {t("dialog.cancel")}
          </Button>
          <Button variant="primary" isPending={submitting} onPress={() => void submit()}>
            {t("imports.submit")}
          </Button>
        </>
      }
    >
      <div className="grid gap-2 p-4 sm:p-5">
        <TextField value={link} onChange={setLink} isRequired>
          <Label>{t("imports.linkLabel")}</Label>
          <Input placeholder="https://t.me/channel/123" autoFocus />
        </TextField>
        <div className="text-xs text-muted">{t("imports.hint")}</div>
      </div>
    </AppDialog>
  );
}
