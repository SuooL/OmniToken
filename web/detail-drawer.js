"use strict";

const DetailDrawer = {
  key: null,
  trigger: null,
  triggerSelector: null,
  dialog: null,
  ensure() {
    if (this.dialog) return this.dialog;
    const dialog = document.createElement("dialog");
    dialog.className = "detail-drawer";
    dialog.id = "tool-detail-drawer";
    dialog.setAttribute("aria-labelledby", "tool-detail-title");
    dialog.innerHTML =
      '<div class="drawer-header"><div><h2 id="tool-detail-title"></h2><p id="tool-detail-subtitle" class="subtle"></p></div><button type="button" class="drawer-close ghost-btn" aria-label="关闭详情并返回">×</button></div><div class="drawer-content"></div>';
    document.body.appendChild(dialog);
    this.dialog = dialog;
    dialog
      .querySelector(".drawer-close")
      .addEventListener("click", () => this.close());
    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      this.close();
    });
    dialog.addEventListener("click", (event) => {
      if (event.target !== dialog) return;
      const rect = dialog.getBoundingClientRect();
      if (
        event.clientX < rect.left ||
        event.clientX > rect.right ||
        event.clientY < rect.top ||
        event.clientY > rect.bottom
      )
        this.close();
    });
    return dialog;
  },
  open({ key, title, subtitle = "", html, trigger }) {
    const dialog = this.ensure();
    const changed = this.key !== key;
    this.key = key;
    this.trigger = trigger || document.activeElement;
    const target = this.trigger;
    const keyAttr = [
      "data-focus-key",
      "data-device-key",
      "data-event-key",
    ].find((attr) => target?.hasAttribute?.(attr));
    this.triggerSelector = target?.id
      ? `#${CSS.escape(target.id)}`
      : keyAttr
        ? `[${keyAttr}="${CSS.escape(target.getAttribute(keyAttr))}"]`
        : null;
    dialog.querySelector("#tool-detail-title").textContent = title;
    dialog.querySelector("#tool-detail-subtitle").textContent = subtitle;
    ToolUI.updateHTML(dialog.querySelector(".drawer-content"), html);
    if (changed) dialog.querySelector(".drawer-content").scrollTop = 0;
    if (!dialog.open) {
      dialog.showModal();
      document.body.classList.add("drawer-open");
    }
    dialog.querySelector(".drawer-close").focus({ preventScroll: true });
  },
  update(key, html) {
    if (this.key !== key || !this.dialog?.open) return;
    ToolUI.updateHTML(this.dialog.querySelector(".drawer-content"), html);
  },
  close({ restoreFocus = true } = {}) {
    if (!this.dialog?.open) return;
    this.dialog.close();
    this.key = null;
    document.body.classList.remove("drawer-open");
    this.dialog.querySelector(".drawer-content").replaceChildren();
    this.dialog.querySelector("#tool-detail-title").textContent = "";
    this.dialog.querySelector("#tool-detail-subtitle").textContent = "";
    const target = this.trigger?.isConnected
      ? this.trigger
      : this.triggerSelector
        ? document.querySelector(this.triggerSelector)
        : null;
    if (restoreFocus)
      (target || document.getElementById("main-content"))?.focus({
        preventScroll: true,
      });
  },
};

document.addEventListener(
  "toggle",
  (event) => {
    if (event.target.tagName !== "DETAILS" || !event.target.open) return;
    requestAnimationFrame(() => {
      resizeChartsIn(event.target);
      ChartRegistry.resizeWithin(event.target);
    });
  },
  true,
);
