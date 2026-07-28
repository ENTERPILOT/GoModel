// Cross-cutting UI state: theme, sidebar collapse, and the count of open
// overlay dialogs (drives the body-level dashboard-modal-open class).

import { readStored, writeStored } from "$lib/utils/storage.ts";

class ThemeStore {
  theme = $state("system");
  // tick increments whenever effective colors may have changed (explicit
  // theme switch, or the OS scheme flipping while in system mode). Charts
  // watch it to rebuild with fresh CSS-variable colors.
  tick = $state(0);

  init(): void {
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

  set(t: string): void {
    this.theme = t;
    writeStored("gomodel_theme", t);
    this.apply();
    this.tick++;
  }

  toggle(): void {
    const order = ["light", "system", "dark"];
    this.set(order[(order.indexOf(this.theme) + 1) % order.length]);
  }

  apply(): void {
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

  init(): void {
    this.collapsed = readStored("gomodel_sidebar_collapsed") === "true";
  }

  toggle(): void {
    this.collapsed = !this.collapsed;
    writeStored("gomodel_sidebar_collapsed", this.collapsed);
  }
}

export const sidebar = new SidebarStore();

// Modal accounting: every overlay dialog registers while open so the app
// shell can add the dashboard-modal-open body class (scroll lock). The
// stack order lets Escape close only the topmost dialog.
class ModalStore {
  stack = $state<number[]>([]);
  #nextToken = 1;

  opened(): number {
    const token = this.#nextToken++;
    this.stack = [...this.stack, token];
    return token;
  }

  closed(token: number): void {
    this.stack = this.stack.filter((t) => t !== token);
  }

  isTop(token: number): boolean {
    return this.stack.length > 0 && this.stack[this.stack.length - 1] === token;
  }

  get openCount(): number {
    return this.stack.length;
  }

  get anyOpen(): boolean {
    return this.stack.length > 0;
  }
}

export const modals = new ModalStore();
