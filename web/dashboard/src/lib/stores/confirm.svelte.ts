// Global typed-confirmation dialog ("type DELETE to confirm"). Pages open it
// with confirmDialog.open({...}); the onConfirm callback runs on submit and
// is responsible for closing the dialog when its action succeeds.

export interface ConfirmDialogState {
  open: boolean;
  title: string;
  titleId: string;
  inputId: string;
  message: string;
  requiredText: string;
  value: string;
  confirmLabel: string;
  icon: string;
  dialogClass: string;
  loading: boolean;
  onConfirm: (() => void | Promise<void>) | null;
  onClose: (() => void) | null;
}

function emptyState(): ConfirmDialogState {
  return {
    open: false,
    title: "",
    titleId: "typedConfirmationDialogTitle",
    inputId: "typed-confirmation-input",
    message: "",
    requiredText: "",
    value: "",
    confirmLabel: "Confirm",
    icon: "triangle-alert",
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

  open(options?: Partial<ConfirmDialogState>): void {
    this.error = "";
    this.state = { ...emptyState(), open: true, ...(options || {}) };
  }

  close(): void {
    const current = this.state;
    if (typeof current.onClose === "function") {
      current.onClose();
    }
    this.state = emptyState();
    this.error = "";
  }

  ready(): boolean {
    return (
      String(this.state.value || "")
        .trim()
        .toLowerCase() ===
      String(this.state.requiredText || "")
        .trim()
        .toLowerCase()
    );
  }

  inputLabel(): string {
    return (
      "Type " + String(this.state.requiredText || "").trim() + " to confirm"
    );
  }

  async submit(): Promise<void> {
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
