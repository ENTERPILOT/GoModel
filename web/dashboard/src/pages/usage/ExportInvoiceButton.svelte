<script>
  // Export invoice (GoModel Pro): downloads the billed usage of the page's
  // window and filters as a per-customer CSV invoice.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { FileText } from "lucide";
  import { apiFetch } from "$lib/api/client.js";
  import { errorPayloadMessage } from "$lib/api/errors.js";
  import { flash } from "$lib/stores/flash.svelte.js";
  import { invoiceExportPath, invoiceFilename } from "./usage-invoice.js";
  import { usageApiQuery } from "./usage-view.js";
  import * as m from "$lib/paraglide/messages.js";

  let exporting = $state(false);

  async function exportInvoice() {
    exporting = true;
    try {
      const res = await apiFetch(invoiceExportPath(usageApiQuery()));
      if (!res.ok) {
        let data = null;
        try {
          data = await res.json();
        } catch {
          data = null;
        }
        flash.error(errorPayloadMessage(data, m.usage_invoice_export_failed()));
        return;
      }
      const url = URL.createObjectURL(await res.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = invoiceFilename(res.headers.get("Content-Disposition"));
      link.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      console.error("Failed to export invoice:", e);
      flash.error(m.usage_invoice_export_failed());
    } finally {
      exporting = false;
    }
  }
</script>

<button
  type="button"
  class="btn btn-with-icon"
  title={m.usage_invoice_export_title()}
  disabled={exporting}
  onclick={exportInvoice}
>
  <Icon icon={FileText} class="form-action-icon" />
  <span>{m.usage_invoice_export()}</span>
</button>
