(() => {
  "use strict";
  const GLOBALS = { turnstile: "turnstile", hcaptcha: "hcaptcha", recaptcha: "grecaptcha" };
  const loaded = {};

  const loadScript = (src) => {
    if (!loaded[src]) {
      loaded[src] = new Promise((resolve, reject) => {
        const s = document.createElement("script");
        s.src = src;
        s.async = true;
        s.onload = resolve;
        s.onerror = () => reject(new Error("captcha script failed to load: " + src));
        document.head.appendChild(s);
      });
    }
    return loaded[src];
  };

  const loadStyle = (href) => {
    const l = document.createElement("link");
    l.rel = "stylesheet";
    l.href = href;
    document.head.appendChild(l);
  };

  const fail = (box, err) => {
    console.error("captcha unavailable", err);
    const msg = box.closest("form").querySelector(".form-error");
    if (msg) {
      msg.textContent = box.dataset.unavailable || "";
      msg.hidden = false;
    }
  };

  const bindExternal = (box, provider) => {
    const form = box.closest("form");
    const submit = form.querySelector('[type="submit"]');
    const field = form.querySelector('input[name="captcha_id"]');
    const set = (token) => {
      field.value = token;
      submit.disabled = !token;
    };
    loadScript(box.dataset.src).then(() => {
      const api = window[GLOBALS[provider]];
      const render = () => api.render(box, {
        sitekey: box.dataset.site,
        callback: (t) => set(t),
        "expired-callback": () => set(""),
        "error-callback": () => {
          set("");
          fail(box, new Error("widget error"));
        },
      });
      if (api.ready) api.ready(render);
      else render();
    }).catch((err) => fail(box, err));
  };

  const bindHosted = (box) => {
    const base = box.dataset.base;
    loadStyle(base + "/static/captcha.css");
    loadScript(base + "/static/captcha.js").then(() => {
      if (window.CProof) window.CProof.bindAll();
    }).catch((err) => fail(box, err));
  };

  window.tvtCaptcha = {
    bindAll() {
      document.querySelectorAll("[data-captcha]:not([data-bound])").forEach((box) => {
        box.dataset.bound = "1";
        const provider = box.dataset.captcha;
        if (provider === "cproof") bindHosted(box);
        else bindExternal(box, provider);
      });
    },
  };
})();
