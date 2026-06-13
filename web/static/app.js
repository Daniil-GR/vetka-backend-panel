(() => {
  const body = document.body;
  const sidebarToggle = document.querySelector("[data-sidebar-toggle]");
  const sidebarClose = document.querySelector("[data-sidebar-close]");
  const copyButtonTimers = new WeakMap();

  function setSidebar(open) {
    if (!body.classList.contains("app-shell")) return;
    body.classList.toggle("sidebar-open", open);
    if (sidebarClose) sidebarClose.hidden = !open;
  }

  sidebarToggle?.addEventListener("click", () => setSidebar(true));
  sidebarClose?.addEventListener("click", () => setSidebar(false));

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      setSidebar(false);
      document.querySelectorAll("dialog[open]").forEach((dialog) => dialog.close());
    }
  });

  document.addEventListener("click", async (event) => {
    const target = event.target instanceof Element ? event.target.closest("[data-copy-text],[data-dialog-open],[data-dialog-close],[data-toast-dismiss],[data-password-toggle],[data-confirm],[data-qr-value],[data-tab-trigger]") : null;
    if (!target) return;

    if (target.hasAttribute("data-copy-text")) {
      const text = target.getAttribute("data-copy-text") || "";
      try {
        await navigator.clipboard.writeText(text);
        const labelNode = target.querySelector("[data-copy-label]");
        const existingTimer = copyButtonTimers.get(target);
        if (existingTimer) {
          window.clearTimeout(existingTimer);
        }
        if (labelNode) {
          const original = target.getAttribute("data-copy-original-label") || labelNode.textContent || "";
          target.setAttribute("data-copy-original-label", original);
          labelNode.textContent = "Copied";
          const timerId = window.setTimeout(() => {
            labelNode.textContent = original;
          }, 1400);
          copyButtonTimers.set(target, timerId);
        } else {
          const original = target.getAttribute("data-copy-original-label") || target.textContent || "";
          target.setAttribute("data-copy-original-label", original);
          target.textContent = "Copied";
          const timerId = window.setTimeout(() => {
            target.textContent = original;
          }, 1400);
          copyButtonTimers.set(target, timerId);
        }
      } catch (_) {}
      return;
    }

    if (target.hasAttribute("data-dialog-open")) {
      const dialog = document.getElementById(target.getAttribute("data-dialog-open"));
      if (dialog && typeof dialog.showModal === "function") dialog.showModal();
      return;
    }

    if (target.hasAttribute("data-dialog-close")) {
      target.closest("dialog")?.close();
      return;
    }

    if (target.hasAttribute("data-toast-dismiss")) {
      target.closest(".toast")?.remove();
      return;
    }

    if (target.hasAttribute("data-password-toggle")) {
      const input = target.parentElement?.querySelector("[data-password-input]");
      if (!(input instanceof HTMLInputElement)) return;
      const nextType = input.type === "password" ? "text" : "password";
      input.type = nextType;
      target.textContent = nextType === "password" ? "Show" : "Hide";
      return;
    }

    if (target.hasAttribute("data-confirm")) {
      const message = target.getAttribute("data-confirm") || "Are you sure?";
      if (!window.confirm(message)) {
        event.preventDefault();
      }
      return;
    }

    if (target.hasAttribute("data-qr-value")) {
      const value = target.getAttribute("data-qr-value") || "";
      const title = target.getAttribute("data-qr-title") || "Subscription QR";
      const dialog = document.getElementById("qr-dialog");
      const canvasHost = document.getElementById("qr-canvas");
      const titleEl = document.getElementById("qr-title");
      const urlEl = document.getElementById("qr-url");
      const copyButton = document.getElementById("qr-copy");
      if (!dialog || !canvasHost || !titleEl || !urlEl || typeof window.qrcode !== "function") return;

      titleEl.textContent = title;
      urlEl.textContent = value;
      if (copyButton) copyButton.setAttribute("data-copy-text", value);
      canvasHost.replaceChildren();
      const qr = window.qrcode(0, "M");
      qr.addData(value);
      qr.make();
      const image = document.createElement("img");
      image.alt = title;
      image.src = qr.createDataURL(6, 12);
      canvasHost.appendChild(image);
      dialog.showModal();
      return;
    }

    if (target.hasAttribute("data-tab-trigger")) {
      const tabsRoot = target.closest("[data-tabs]")?.parentElement;
      if (!tabsRoot) return;
      const name = target.getAttribute("data-tab-trigger");
      tabsRoot.querySelectorAll("[data-tab-trigger]").forEach((tab) => tab.classList.toggle("is-active", tab === target));
      tabsRoot.querySelectorAll("[data-tab-panel]").forEach((panel) => panel.classList.toggle("is-active", panel.getAttribute("data-tab-panel") === name));
    }
  });

  document.querySelectorAll("[data-protocol-select]").forEach((select) => {
    const apply = () => {
      const root = select.closest("form, .tab-panel, .panel, .dialog") || document;
      const value = select.value;
      root.querySelectorAll("[data-protocol-panel]").forEach((panel) => {
        panel.classList.toggle("is-hidden", panel.getAttribute("data-protocol-panel") !== value);
      });
    };
    select.addEventListener("change", apply);
    apply();
  });

  window.setTimeout(() => {
    document.querySelectorAll(".toast-success, .toast-info").forEach((toast) => toast.remove());
  }, 3200);
})();
