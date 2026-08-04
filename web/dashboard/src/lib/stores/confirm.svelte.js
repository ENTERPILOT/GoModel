// Global typed-confirmation dialog ("type DELETE to confirm"). Pages open it
// with confirmDialog.open({...}); the onConfirm callback runs on submit and
// is responsible for closing the dialog when its action succeeds.

import { TriangleAlert } from "lucide";

function emptyState() {
  return {
    open: false,
    title: "",
    titleId: "typedConfirmationDialogTitle",
    inputId: "typed-confirmation-input",
    message: "",
    requiredText: "",
    value: "",
    confirmLabel: "Confirm",
    icon: TriangleAlert,
    dialogClass: "",
    loading: false,
    onConfirm: null,
    onClose: null,
  };
}

class ConfirmDialogStore {
  state = $state(emptyState());
  // Inline error text surfaced by the opener, kept local to the dialog.
  error = $state("");

  open(options) {
    this.error = "";
    this.state = { ...emptyState(), open: true, ...(options || {}) };
  }

  close() {
    const current = this.state;
    if (typeof current.onClose === "function") {
      current.onClose();
    }
    this.state = emptyState();
    this.error = "";
  }

  ready() {
    return (
      String(this.state.value || "")
        .trim()
        .toLowerCase() ===
      String(this.state.requiredText || "")
        .trim()
        .toLowerCase()
    );
  }

  inputLabel() {
    return (
      "Type " + String(this.state.requiredText || "").trim() + " to confirm"
    );
  }

  async submit() {
    if (!this.ready()) {
      this.error = this.inputLabel() + ".";
      return;
    }
    if (typeof this.state.onConfirm === "function") {
      this.state.loading = true;
      try {
        await this.state.onConfirm();
      } finally {
        this.state.loading = false;
      }
    }
  }
}

export const confirmDialog = new ConfirmDialogStore();
