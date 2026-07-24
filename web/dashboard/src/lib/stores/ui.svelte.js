// Cross-cutting UI state: theme, sidebar collapse, and the count of open
// overlay dialogs (drives the body-level dashboard-modal-open class).

class ThemeStore {
  theme = $state("system");
  // tick increments whenever effective colors may have changed (explicit
  // theme switch, or the OS scheme flipping while in system mode). Charts
  // watch it to rebuild with fresh CSS-variable colors.
  tick = $state(0);

  init() {
    try {
      this.theme = localStorage.getItem("gomodel_theme") || "system";
    } catch {
      this.theme = "system";
    }
    this.apply();
    window
      .matchMedia("(prefers-color-scheme: dark)")
      .addEventListener("change", () => {
        if (this.theme === "system") {
          this.tick++;
        }
      });
  }

  set(t) {
    this.theme = t;
    try {
      localStorage.setItem("gomodel_theme", t);
    } catch {
      // Non-fatal: theme just won't persist.
    }
    this.apply();
    this.tick++;
  }

  toggle() {
    const order = ["light", "system", "dark"];
    this.set(order[(order.indexOf(this.theme) + 1) % order.length]);
  }

  apply() {
    const root = document.documentElement;
    if (this.theme === "system") {
      root.removeAttribute("data-theme");
    } else {
      root.setAttribute("data-theme", this.theme);
    }
  }
}

export const themeStore = new ThemeStore();

class SidebarStore {
  collapsed = $state(false);

  init() {
    try {
      this.collapsed = localStorage.getItem("gomodel_sidebar_collapsed") === "true";
    } catch {
      this.collapsed = false;
    }
  }

  toggle() {
    this.collapsed = !this.collapsed;
    try {
      localStorage.setItem("gomodel_sidebar_collapsed", String(this.collapsed));
    } catch {
      // Non-fatal.
    }
  }
}

export const sidebar = new SidebarStore();

// Modal accounting: every overlay dialog registers while open so the app
// shell can add the dashboard-modal-open body class (scroll lock). The
// stack order lets Escape close only the topmost dialog.
class ModalStore {
  stack = $state([]);
  #nextToken = 1;

  opened() {
    const token = this.#nextToken++;
    this.stack = [...this.stack, token];
    return token;
  }

  closed(token) {
    this.stack = this.stack.filter((t) => t !== token);
  }

  isTop(token) {
    return this.stack.length > 0 && this.stack[this.stack.length - 1] === token;
  }

  get openCount() {
    return this.stack.length;
  }

  get anyOpen() {
    return this.stack.length > 0;
  }
}

export const modals = new ModalStore();
