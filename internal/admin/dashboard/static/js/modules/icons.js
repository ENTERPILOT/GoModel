(function (global) {
  function dashboardIconsModule() {
    return {
      renderIcons(root) {
        const iconRoot =
          root && typeof root.querySelectorAll === "function"
            ? root
            : global.document;
        const lucide = global.lucide;

        if (
          !iconRoot ||
          !lucide ||
          typeof lucide.createIcons !== "function"
        ) {
          return false;
        }

        lucide.createIcons({
          root: iconRoot,
          attrs: {
            "aria-hidden": "true",
            focusable: "false",
          },
        });
        return true;
      },

      renderIconsAfterUpdate(root) {
        const render = () => this.renderIcons(root);
        if (typeof this.$nextTick === "function") {
          this.$nextTick(render);
          return;
        }
        setTimeout(render, 0);
      },
    };
  }

  global.dashboardIconsModule = dashboardIconsModule;
})(typeof window !== "undefined" ? window : globalThis);
