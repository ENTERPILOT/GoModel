// Cross-cutting UI state: theme, sidebar sizing, and the count of open
// overlay dialogs (drives the body-level dashboard-modal-open class).

import { readStored, writeStored } from "$lib/utils/storage.js";
import {
  MAX_SIDEBAR_WIDTH,
  MIN_SIDEBAR_WIDTH,
  clampSidebarWidth,
} from "./sidebar-sizing.js";

class ThemeStore {
  theme = $state("system");
  // tick increments whenever effective colors may have changed (explicit
  // theme switch, or the OS scheme flipping while in system mode). Charts
  // watch it to rebuild with fresh CSS-variable colors.
  tick = $state(0);

  init() {
    this.theme = readStored("gomodel_theme", "system");
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
    writeStored("gomodel_theme", t);
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
  width = $state(MAX_SIDEBAR_WIDTH);

  get collapsed() {
    return this.width === MIN_SIDEBAR_WIDTH;
  }

  init() {
    const storedWidth = readStored("gomodel_sidebar_width");
    const legacyCollapsed = readStored("gomodel_sidebar_collapsed") === "true";
    this.setWidth(
      storedWidth == null
        ? (legacyCollapsed ? MIN_SIDEBAR_WIDTH : MAX_SIDEBAR_WIDTH)
        : storedWidth,
    );
  }

  toggle() {
    this.setWidth(
      this.collapsed ? MAX_SIDEBAR_WIDTH : MIN_SIDEBAR_WIDTH,
      true,
    );
  }

  setWidth(value, persist = false) {
    this.width = clampSidebarWidth(value);
    if (typeof document !== "undefined") {
      document.documentElement.style.setProperty("--sidebar-width", `${this.width}px`);
    }
    if (!persist) return;
    writeStored("gomodel_sidebar_width", this.width);
    writeStored("gomodel_sidebar_collapsed", this.collapsed);
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
