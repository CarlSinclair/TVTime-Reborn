(function () {
  "use strict";

  const textOf = (container, key, fallback) => (container && container.dataset && container.dataset[key]) || fallback;

  function getCSRFToken() {
    const meta = document.querySelector('meta[name="csrf"]');
    return meta && meta.content ? meta.content : "";
  }

  const CAPTCHA_TTL_MS = 5 * 60 * 1000;
  const resetTimers = new WeakMap();

  function encodeForm(obj) {
    return Object.keys(obj)
      .map(k => encodeURIComponent(k) + "=" + encodeURIComponent(obj[k]))
      .join("&");
  }

  function resolveContainer(trigger) {
    if (!trigger) return null;
    if (trigger.classList && trigger.classList.contains("captcha-game-container")) return trigger;
    if (trigger.closest) {
      const c = trigger.closest(".captcha-game-container");
      if (c) return c;
    }
    if (trigger.querySelector) {
      const direct = trigger.querySelector(".captcha-game-container");
      if (direct) return direct;
    }
    return document.querySelector(".captcha-game-container");
  }

  function clearResetTimer(container) {
    const t = resetTimers.get(container);
    if (t) {
      clearTimeout(t);
      resetTimers.delete(container);
    }
  }

  function triggerExpiredWarning(container) {
    const instr = container.querySelector(".captcha-instructions p");
    if (instr) {
      const old = instr.dataset.origText || instr.innerText;
      instr.dataset.origText = old;
      instr.innerText = textOf(container, "i18nExpired", "Expired. Refreshing...");
      instr.style.color = "var(--danger)";
      setTimeout(() => {
        instr.innerText = old;
        instr.style.color = "";
      }, 3000);
    }
  }

  function scheduleExpiryReset(container) {
    clearResetTimer(container);
    const t = setTimeout(() => {
      setSolvedUI(container, false);
      setSubmitEnabled(container, false);
      triggerExpiredWarning(container);
      refreshCaptcha(container);
    }, CAPTCHA_TTL_MS);
    resetTimers.set(container, t);
  }

  function setSubmitEnabled(container, enabled) {
    const form = container.closest("form");
    if (!form) return;

    const tooltip = (container.dataset && container.dataset.i18nCompleteFirst) ? container.dataset.i18nCompleteFirst : "";
    const buttons = form.querySelectorAll('button[type="submit"], input[type="submit"]');

    buttons.forEach(btn => {
      btn.disabled = !enabled;
      if (!enabled) {
        btn.classList.add("is-disabled");
        btn.setAttribute("aria-disabled", "true");
        if (tooltip) btn.title = tooltip;
      } else {
        btn.classList.remove("is-disabled");
        btn.removeAttribute("aria-disabled");
        if (tooltip) btn.removeAttribute("title");
      }
    });
  }

  function setSolvedUI(container, solved) {
    const view = container.querySelector(".captcha-game-view");
    const successView = container.querySelector(".captcha-success-view");
    const slider = container.querySelector(".captcha-slider");
    const refreshBtn = container.querySelector(".refresh-captcha-btn");
    const wrapper = container.querySelector(".captcha-image-wrapper");

    if (solved) {
      container.classList.add("solved");
      if (view) view.style.display = "none";
      if (successView) {
        successView.style.display = "flex";
        successView.setAttribute("aria-hidden", "false");
      }
      if (slider) slider.disabled = true;
      if (refreshBtn) refreshBtn.disabled = true;
      if (wrapper) wrapper.setAttribute("aria-disabled", "true");
    } else {
      container.classList.remove("solved");
      if (view) view.style.display = "";
      if (successView) {
        successView.style.display = "none";
        successView.setAttribute("aria-hidden", "true");
      }
      if (slider) slider.disabled = false;
      if (refreshBtn) refreshBtn.disabled = false;
      if (wrapper) wrapper.removeAttribute("aria-disabled");
    }
  }

  async function computePoW(salt, difficulty) {
    if (!salt || difficulty <= 0) return "";
    const prefix = "0".repeat(difficulty);
    const encoder = new TextEncoder();
    let nonce = 0;
    while (true) {
      for (let i = 0; i < 500; i++) {
        const data = encoder.encode(salt + nonce);
        const hashBuffer = await crypto.subtle.digest('SHA-256', data);
        const hashArray = new Uint8Array(hashBuffer);
        
        let hexStr = "";
        for(let j=0; j<hashArray.length; j++) {
            hexStr += hashArray[j].toString(16).padStart(2, '0');
            if (hexStr.length >= difficulty) break; 
        }
        if (hexStr.startsWith(prefix)) {
          return String(nonce);
        }
        nonce++;
      }
      await new Promise(r => setTimeout(r, 0));
    }
  }

  async function processCompositeID(container, compositeID) {
    if (!compositeID) return;
    
    const parts = compositeID.split("_");
    if (parts.length === 4) {
      const realId = parts[0];
      const salt = parts[1];
      const diff = parseInt(parts[2], 10);
      const visReq = parts[3];

      container.dataset.captchaId = compositeID;
      container.dataset.powSalt = salt;
      container.dataset.powDiff = diff;
      container.dataset.visualReq = visReq;
      
      const idInput = container.querySelector('input[name="captcha_id"]');
      if (idInput) idInput.value = compositeID;

      const nonceInput = container.querySelector('input[name="pow_nonce"]');
      if (nonceInput) nonceInput.value = "";
      container.dataset.powNonce = "";

      if (visReq === "0") {
        const view = container.querySelector(".captcha-game-view");
        const successView = container.querySelector(".captcha-success-view");
        if (view) view.style.display = "none";
        if (successView) {
            successView.style.display = "flex";
            const p = successView.querySelector("p");
            if (p) p.innerText = textOf(container, "i18nSecuring", "Securing connection...");
        }

        const nonce = await computePoW(salt, diff);
        container.dataset.powNonce = nonce;
        if (nonceInput) nonceInput.value = nonce;
        
        await verifyCaptcha(container, "0", true);
      } else {
        const nonce = await computePoW(salt, diff);
        container.dataset.powNonce = nonce;
        if (nonceInput) nonceInput.value = nonce;
      }
    }
  }

  async function refreshCaptcha(triggerOrContainer) {
    const container = resolveContainer(triggerOrContainer);
    if (!container) return;

    const img = container.querySelector(".captcha-image-main");
    const idInput = container.querySelector('input[name="captcha_id"]');
    const slider = container.querySelector(".captcha-slider");

    if (!img || !idInput) return;

    clearResetTimer(container);
    container.classList.remove("success-flash", "shake");
    setSolvedUI(container, false);
    setSubmitEnabled(container, false);

    try {
      const resp = await fetch("/api/captcha/new", {
        method: "GET",
        headers: { "X-Requested-With": "XMLHttpRequest" }
      });

      const data = await resp.json();
      if (!data || !data.id || !data.image) return;

      img.src = data.image;
      img.style.transform = "rotate(0deg)";

      if (slider) {
        slider.disabled = false;
        slider.value = "0";
      }

      await processCompositeID(container, data.id);
    } catch (err) {
      console.warn("captcha refresh failed", err);
    }
  }

  let verifyTimer = null;

  function updateRotation(container, value) {
    const v = (Math.round(Number(value)) % 360 + 360) % 360;
    const img = container.querySelector(".captcha-image-main");
    if (img) img.style.transform = `rotate(${v}deg)`;

    if (verifyTimer) clearTimeout(verifyTimer);
    verifyTimer = setTimeout(() => verifyCaptcha(container, String(v), false), 750);
  }

  async function verifyCaptcha(container, value, committed) {
    const id = container.dataset.captchaId || "";
    if (!id) return;

    const nonceInput = container.querySelector('input[name="pow_nonce"]');
    const nonce = nonceInput ? nonceInput.value : container.dataset.powNonce;

    const csrf = getCSRFToken();

    container.classList.remove("success-flash", "shake");

    try {
      const resp = await fetch("/api/captcha/verify", {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          "X-Requested-With": "XMLHttpRequest",
          "X-CSRF-Token": csrf
        },
        body: encodeForm({ captcha_id: id, captcha_solution: value, pow_nonce: nonce || "", committed: committed ? "1" : "0" })
      });

      const data = await resp.json();

      if (data && data.success) {
        container.classList.add("success-flash");
        setSolvedUI(container, true);
        
        const successView = container.querySelector(".captcha-success-view");
        if (successView) {
            const p = successView.querySelector("p");
            if (p && container.dataset.visualReq === "0") {
                p.innerText = textOf(container, "i18nSecured", "Secured.");
            }
        }

        setSubmitEnabled(container, true);
        scheduleExpiryReset(container);
      } else if (data && data.expired) {
        setSolvedUI(container, false);
        setSubmitEnabled(container, false);
        triggerExpiredWarning(container);
        refreshCaptcha(container);
      } else {
        if (container.dataset.visualReq === "1") {
            setSolvedUI(container, false);
            setSubmitEnabled(container, false);
            container.classList.add("shake");
        }
      }
    } catch (_) {
      if (container.dataset.visualReq === "1") {
          setSolvedUI(container, false);
          setSubmitEnabled(container, false);
          container.classList.add("shake");
      }
    }
  }

  document.addEventListener("click", (e) => {
    const btn = e.target.closest(".refresh-captcha-btn");
    if (!btn) return;
    e.preventDefault();
    refreshCaptcha(btn);
  });

  document.addEventListener("input", (e) => {
    const slider = e.target.closest(".captcha-slider");
    if (!slider || slider.disabled) return;
    const container = slider.closest(".captcha-game-container");
    if (!container) return;
    updateRotation(container, slider.value);
  });

  document.addEventListener("change", (e) => {
    const slider = e.target.closest(".captcha-slider");
    if (!slider || slider.disabled) return;
    const container = slider.closest(".captcha-game-container");
    if (!container) return;
    if (verifyTimer) clearTimeout(verifyTimer);
    verifyCaptcha(container, slider.value, true);
  });

  let isDragging = false;
  let startMouseAngle = 0;
  let baseAngle = 0;
  let dragContainer = null;
  let dragSlider = null;
  let dragRect = null;

  function getAngle(x, y, centerX, centerY) {
    const radians = Math.atan2(y - centerY, x - centerX);
    return radians * (180 / Math.PI);
  }

  function startDrag(e) {
    const wrapper = e.target.closest(".captcha-image-wrapper");
    if (!wrapper) return;

    const container = wrapper.closest(".captcha-game-container");
    if (!container) return;

    const slider = container.querySelector(".captcha-slider");
    if (!slider || slider.disabled) return;

    e.preventDefault();

    dragRect = wrapper.getBoundingClientRect();
    const centerX = dragRect.left + dragRect.width / 2;
    const centerY = dragRect.top + dragRect.height / 2;

    const clientX = e.type === "touchstart" ? e.touches[0].clientX : e.clientX;
    const clientY = e.type === "touchstart" ? e.touches[0].clientY : e.clientY;

    isDragging = true;
    dragContainer = container;
    dragSlider = slider;

    startMouseAngle = getAngle(clientX, clientY, centerX, centerY);
    baseAngle = parseInt(slider.value, 10) || 0;

    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onEnd);
    document.addEventListener("touchmove", onMove, { passive: false });
    document.addEventListener("touchend", onEnd);
    document.addEventListener("touchcancel", onEnd);
  }

  function onMove(moveEvent) {
    if (!isDragging || !dragContainer || !dragSlider || !dragRect) return;

    moveEvent.preventDefault();

    const centerX = dragRect.left + dragRect.width / 2;
    const centerY = dragRect.top + dragRect.height / 2;

    const mX = moveEvent.type === "touchmove" ? moveEvent.touches[0].clientX : moveEvent.clientX;
    const mY = moveEvent.type === "touchmove" ? moveEvent.touches[0].clientY : moveEvent.clientY;

    const currentMouseAngle = getAngle(mX, mY, centerX, centerY);
    let diff = currentMouseAngle - startMouseAngle;

    let newAngle = (baseAngle + diff) % 360;
    if (newAngle < 0) newAngle += 360;

    newAngle = Math.round(newAngle) % 360;

    dragSlider.value = String(newAngle);
    updateRotation(dragContainer, newAngle);
  }

  function onEnd() {
    const container = dragContainer;
    const slider = dragSlider;

    isDragging = false;
    dragContainer = null;
    dragSlider = null;
    dragRect = null;

    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onEnd);
    document.removeEventListener("touchmove", onMove);
    document.removeEventListener("touchend", onEnd);
    document.removeEventListener("touchcancel", onEnd);

    if (container && slider) {
      if (verifyTimer) clearTimeout(verifyTimer);
      verifyCaptcha(container, slider.value, true);
    }
  }

  document.addEventListener("mousedown", startDrag);
  document.addEventListener("touchstart", startDrag, { passive: false });

  function bindContainer(container) {
    if (!container || container.dataset.captchaBound === "1") return;
    container.dataset.captchaBound = "1";

    container.classList.remove("success-flash", "shake");
    setSolvedUI(container, false);
    setSubmitEnabled(container, false);

    const idInput = container.querySelector('input[name="captcha_id"]');
    const img = container.querySelector(".captcha-image-main");

    const hasServerCaptcha = !!(idInput && idInput.value && img && img.getAttribute("src"));
    if (!hasServerCaptcha) {
      refreshCaptcha(container);
    } else {
      if (img) img.style.transform = "rotate(0deg)";
      const slider = container.querySelector(".captcha-slider");
      if (slider) slider.value = "0";
      
      processCompositeID(container, idInput.value);
    }
  }

  function bindAll() {
    const nodes = document.querySelectorAll(".captcha-game-container");
    if (!nodes.length) return;
    nodes.forEach(bindContainer);
  }

  document.addEventListener("tvt:form-done", (event) => {
    const form = event.detail;
    if (!form || !form.querySelectorAll) return;
    form.querySelectorAll(".captcha-game-container").forEach((c) => refreshCaptcha(c));
  });

  window.tvtCaptcha = {
    bindAll,
    refresh: (el) => refreshCaptcha(el)
  };
})();
