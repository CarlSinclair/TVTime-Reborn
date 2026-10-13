(() => {
  const csrfMeta = document.querySelector('meta[name="csrf"]');
  const csrf = csrfMeta ? csrfMeta.content : "";
  const lang = document.documentElement.lang || undefined;
  const loggedIn = document.body.dataset.user === "1";
  let strings = {};
  const i18nNode = document.getElementById("i18n");
  if (i18nNode) {
    try {
      strings = JSON.parse(i18nNode.textContent);
    } catch (err) {
      console.error("i18n parse failed", err);
    }
  }
  const t = (key) => strings[key] || key;
  const fmt = (key, ...args) => args.reduce((s, a) => s.replace("%d", a), t(key));

  const toast = (message, kind, link) => {
    const box = document.getElementById("toasts");
    if (!box) return;
    const el = document.createElement(link ? "a" : "div");
    el.className = "toast" + (kind ? " " + kind : "");
    if (link) el.href = link;
    el.textContent = message;
    box.appendChild(el);
    setTimeout(() => el.remove(), kind === "error" ? 6000 : 4500);
  };

  const parse = async (res, path) => {
    let data = null;
    try {
      data = await res.json();
    } catch (err) {
      console.error("bad response", path, res.status, err);
      throw new Error(t("network_error"));
    }
    if (!res.ok || !data.ok) throw new Error(data && data.error ? data.error : t("network_error"));
    return data;
  };

  const api = async (path, body, method) => {
    let res;
    try {
      res = await fetch(path, {
        method: method || "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: method === "GET" ? undefined : JSON.stringify(body || {}),
        credentials: "same-origin",
      });
    } catch (err) {
      console.error("network error", path, err);
      throw new Error(t("network_error"));
    }
    return parse(res, path);
  };

  const upload = async (path, file) => {
    const form = new FormData();
    form.append("file", file);
    let res;
    try {
      res = await fetch(path, { method: "POST", headers: { "X-CSRF-Token": csrf }, body: form, credentials: "same-origin" });
    } catch (err) {
      console.error("upload error", path, err);
      throw new Error(t("network_error"));
    }
    return parse(res, path);
  };

  const run = async (el, fn) => {
    if (el) el.disabled = true;
    try {
      return await fn();
    } catch (err) {
      toast(err.message, "error");
      return null;
    } finally {
      if (el) el.disabled = false;
    }
  };

  const nextTarget = () => {
    const n = new URLSearchParams(window.location.search).get("next");
    return n && n.startsWith("/") && !n.startsWith("//") && !n.startsWith("/\\") ? n : null;
  };

  const go = (data) => {
    if (data && data.redirect) {
      window.location.href = (data.redirect === "/" && nextTarget()) || data.redirect;
    } else {
      window.location.reload();
    }
  };

  const formBody = (form) => {
    const body = {};
    Array.from(form.elements).forEach((el) => {
      if (!el.name || el.disabled) return;
      if (el.type === "checkbox") body[el.name] = el.checked;
      else if (el.type !== "submit" && el.type !== "button") body[el.name] = el.value;
    });
    return body;
  };

  const showRecovery = (codes) => {
    const box = document.getElementById("recovery-box");
    const pre = document.getElementById("recovery-codes");
    if (!box || !pre) return;
    pre.textContent = codes.join("\n");
    box.hidden = false;
    box.scrollIntoView({ behavior: "smooth", block: "center" });
  };

  document.addEventListener("submit", async (event) => {
    const form = event.target.closest("form[data-api]");
    if (!form) return;
    event.preventDefault();
    if (form.dataset.confirm && !window.confirm(form.dataset.confirm)) return;
    const errBox = form.querySelector(".form-error");
    if (errBox) errBox.hidden = true;
    const submit = form.querySelector('[type="submit"]');
    const data = await run(submit, async () => {
      try {
        return await api(form.dataset.api, formBody(form));
      } catch (err) {
        if (errBox) {
          errBox.textContent = err.message;
          errBox.hidden = false;
        }
        throw err;
      }
    });
    if (!data) return;
    if (data.mail_sent === false) toast(t("mail_failed"), "error");
    if (data.recovery_codes) {
      showRecovery(data.recovery_codes);
      form.reset();
      return;
    }
    if (data.redirect) {
      go(data);
    } else if (form.dataset.reload) {
      toast(form.dataset.success || t("saved"), "ok");
      setTimeout(() => window.location.reload(), 700);
    } else if (form.dataset.success) {
      toast(form.dataset.success, "ok");
      form.reset();
    }
  });

  const tmdbOf = (el) => {
    const box = el.closest("[data-tmdb]");
    return box ? box.dataset.tmdb : null;
  };

  const urlKey = (b64) => {
    const pad = "=".repeat((4 - (b64.length % 4)) % 4);
    const raw = atob((b64 + pad).replace(/-/g, "+").replace(/_/g, "/"));
    return Uint8Array.from(raw, (c) => c.charCodeAt(0));
  };

  const pushSupported = () => "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;

  const pushEnable = async (el) => {
    if (!pushSupported()) {
      toast(t("push_unsupported"), "error");
      return;
    }
    const data = await run(el, async () => {
      const perm = await Notification.requestPermission();
      if (perm !== "granted") throw new Error(t("push_denied"));
      const reg = await navigator.serviceWorker.ready;
      const key = (await api("/api/push/key", null, "GET")).key;
      const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlKey(key) });
      return api("/api/push/subscribe", sub.toJSON());
    });
    if (data) {
      toast(t("push_enabled"), "ok");
      setTimeout(() => window.location.reload(), 700);
    }
  };

  const pushDisable = async (el) => {
    const data = await run(el, async () => {
      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.getSubscription();
      if (!sub) return { ok: true };
      const endpoint = sub.endpoint;
      await sub.unsubscribe();
      return api("/api/push/unsubscribe", { endpoint });
    });
    if (data) window.location.reload();
  };

  let tgPoll = null;
  const telegramConnect = async (el) => {
    const data = await run(el, () => api("/api/account/telegram/link-token"));
    if (!data) return;
    window.location.href = data.url;
    const waiting = document.getElementById("telegram-waiting");
    if (waiting) waiting.hidden = false;
    clearInterval(tgPoll);
    const started = Date.now();
    tgPoll = setInterval(async () => {
      if (Date.now() - started > 10 * 60 * 1000) {
        clearInterval(tgPoll);
        return;
      }
      try {
        const st = await api("/api/account/telegram/status", null, "GET");
        if (st.chat_linked) {
          clearInterval(tgPoll);
          window.location.reload();
        }
      } catch (err) {
        console.warn("telegram status poll failed", err);
      }
    }, 2500);
  };

  document.addEventListener("click", async (event) => {
    const el = event.target.closest("button, a");
    if (!el) return;
    const d = el.dataset;

    if ("logout" in d) {
      const data = await run(el, () => api("/api/auth/logout"));
      if (data) go(data);
      return;
    }
    if ("logoutEverywhere" in d) {
      const data = await run(el, () => api("/api/account/logout-everywhere"));
      if (data) go(data);
      return;
    }
    if ("resendVerify" in d) {
      const data = await run(el, () => api("/api/auth/resend-verify"));
      if (data) toast(t("verify_sent"), "ok");
      return;
    }
    if ("magic" in d) {
      const form = el.closest("form");
      const email = form.querySelector('[name="email"]').value.trim();
      if (!email) {
        form.querySelector('[name="email"]').focus();
        return;
      }
      const data = await run(el, () => api("/api/auth/magic", { email }));
      if (data) toast(el.dataset.success, "ok");
      return;
    }
    if ("follow" in d) {
      const data = await run(el, () => api("/api/show/" + tmdbOf(el) + "/follow", { status: "watching" }));
      if (data) window.location.reload();
      return;
    }
    if ("unfollow" in d) {
      const data = await run(el, () => api("/api/show/" + tmdbOf(el) + "/unfollow"));
      if (data) window.location.reload();
      return;
    }
    if ("favorite" in d) {
      const next = d.favorite !== "1";
      const data = await run(el, () => api("/api/show/" + tmdbOf(el) + "/favorite", { favorite: next }));
      if (data) window.location.reload();
      return;
    }
    if ("watch" in d) {
      const watched = d.state !== "1";
      const data = await run(el, () => api("/api/episode/" + d.watch + "/watch", { watched }));
      if (!data) return;
      if (el.closest(".upnext-item")) {
        window.location.reload();
        return;
      }
      el.dataset.state = watched ? "1" : "0";
      if (el.classList.contains("watch-btn")) {
        el.setAttribute("aria-pressed", watched ? "true" : "false");
      } else if (el.dataset.labelOn) {
        el.textContent = watched ? el.dataset.labelOn : el.dataset.labelOff;
      }
      return;
    }
    if ("watchUpto" in d) {
      const data = await run(el, () => api("/api/episode/" + d.watchUpto + "/watch", { upTo: true }));
      if (data) {
        toast(fmt("marked_many", data.marked), "ok");
        window.location.reload();
      }
      return;
    }
    if ("seasonWatch" in d) {
      const box = el.closest("[data-season]");
      const data = await run(el, () =>
        api("/api/show/" + box.dataset.tmdb + "/season/" + box.dataset.season + "/watch", { watched: d.seasonWatch === "1" })
      );
      if (data) window.location.reload();
      return;
    }
    if ("unblock" in d) {
      const data = await run(el, () => api("/api/blocks/unblock", { username: d.unblock }));
      if (data) window.location.reload();
      return;
    }
    if ("adminResolve" in d) {
      const data = await run(el, () => api("/api/admin/comments/" + d.adminResolve + "/resolve"));
      if (data) window.location.reload();
      return;
    }
    if ("adminDelete" in d) {
      if (!window.confirm(t("delete_confirm"))) return;
      const data = await run(el, () => api("/api/admin/comments/" + d.adminDelete + "/delete"));
      if (data) window.location.reload();
      return;
    }
    if ("adminBan" in d) {
      if (!window.confirm(t("ban_confirm"))) return;
      const data = await run(el, () => api("/api/admin/users/" + d.adminBan + "/ban", { banned: true }));
      if (data) window.location.reload();
      return;
    }
    if (el.id === "push-enable") return pushEnable(el);
    if (el.id === "push-disable") return pushDisable(el);
    if (el.id === "telegram-connect") return telegramConnect(el);
    if (el.id === "telegram-dm") {
      const data = await run(el, () => api("/api/account/telegram/enable-dm"));
      if (data) window.location.reload();
      return;
    }
    if ("telegramUnlinkChat" in d) {
      const data = await run(el, () => api("/api/account/telegram/unlink-chat"));
      if (data) window.location.reload();
      return;
    }
    if ("telegramUnlink" in d) {
      if (!window.confirm(t("telegram_unlink_confirm"))) return;
      const data = await run(el, () => api("/api/account/telegram/unlink"));
      if (data) window.location.reload();
      return;
    }
    if ("testChannel" in d) {
      const data = await run(el, () => api("/api/notifications/test", { channel: d.testChannel }));
      if (data) toast(t("test_sent"), "ok");
      return;
    }
    if (el.id === "timing-save") {
      const preset = document.getElementById("timing-preset").value;
      let minutes;
      if (preset === "custom") {
        minutes = Math.round(parseFloat(document.getElementById("timing-value").value) * parseInt(document.getElementById("timing-unit").value, 10));
      } else {
        minutes = parseInt(preset, 10);
      }
      const errBox = document.getElementById("timing-error");
      errBox.hidden = true;
      if (!Number.isFinite(minutes)) {
        errBox.textContent = t("invalid_timing");
        errBox.hidden = false;
        return;
      }
      const data = await run(el, async () => {
        try {
          return await api("/api/notifications/timing", { offset_min: minutes });
        } catch (err) {
          errBox.textContent = err.message;
          errBox.hidden = false;
          throw err;
        }
      });
      if (data) toast(t("saved"), "ok");
      return;
    }
    if (el.id === "tz-detect") {
      const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
      const select = document.getElementById("settings-tz");
      if (!zone || !select) return;
      if (!Array.from(select.options).some((o) => o.value === zone)) {
        const opt = document.createElement("option");
        opt.value = zone;
        opt.textContent = zone;
        select.appendChild(opt);
      }
      select.value = zone;
      select.dispatchEvent(new Event("change", { bubbles: true }));
      return;
    }
    if (el.id === "twofa-start") {
      const data = await run(el, () => api("/api/account/2fa/setup"));
      if (!data) return;
      document.getElementById("twofa-qr").src = data.qr;
      document.getElementById("twofa-secret").textContent = data.secret;
      document.getElementById("twofa-setup").hidden = false;
      el.hidden = true;
      return;
    }
    if (el.id === "recovery-copy") {
      const text = document.getElementById("recovery-codes").textContent;
      try {
        await navigator.clipboard.writeText(text);
        toast(t("copied"), "ok");
      } catch (err) {
        console.warn("clipboard unavailable", err);
        toast(t("copy_failed"), "error");
      }
      return;
    }
    if (el.id === "recovery-done") {
      window.location.reload();
      return;
    }
    if ("passkeyDelete" in d) {
      if (!window.confirm(t("passkey_remove_confirm"))) return;
      const data = await run(el, () => api("/api/account/passkey/" + d.passkeyDelete + "/delete"));
      if (data) window.location.reload();
      return;
    }
    if ("removeMedia" in d) {
      const data = await run(el, () => api("/api/profile/" + (d.removeMedia === "avatars" ? "avatar" : "banner") + "/remove"));
      if (data) window.location.reload();
    }
  });

  const timingBox = document.getElementById("timing-box");
  if (timingBox) {
    const preset = document.getElementById("timing-preset");
    const custom = document.getElementById("timing-custom");
    const value = document.getElementById("timing-value");
    const unit = document.getElementById("timing-unit");
    const sync = () => {
      custom.hidden = preset.value !== "custom";
    };
    const minutes = parseInt(timingBox.dataset.offset, 10) || 0;
    if (preset.value === "custom") {
      const abs = Math.abs(minutes);
      const u = abs % 1440 === 0 && abs !== 0 ? 1440 : abs % 60 === 0 && abs !== 0 ? 60 : 1;
      unit.value = String(u);
      value.value = String(minutes / u);
    }
    preset.addEventListener("change", sync);
    sync();
  }

  const twofaConfirm = document.getElementById("twofa-confirm");
  if (twofaConfirm) {
    twofaConfirm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const errBox = twofaConfirm.querySelector(".form-error");
      errBox.hidden = true;
      const submit = twofaConfirm.querySelector('[type="submit"]');
      const data = await run(submit, async () => {
        try {
          return await api("/api/account/2fa/enable", formBody(twofaConfirm));
        } catch (err) {
          errBox.textContent = err.message;
          errBox.hidden = false;
          throw err;
        }
      });
      if (data) {
        document.getElementById("twofa-setup").hidden = true;
        showRecovery(data.recovery_codes);
      }
    });
  }

  document.addEventListener("change", async (event) => {
    const el = event.target;
    if (el.matches("[data-follow-status]")) {
      const data = await run(el, () => api("/api/show/" + tmdbOf(el) + "/follow", { status: el.value }));
      if (data) toast(t("saved"), "ok");
    } else if (el.matches("[data-rate]")) {
      const data = await run(el, () => api("/api/episode/" + el.dataset.rate + "/rate", { rating: parseInt(el.value, 10) }));
      if (data) toast(t("saved"), "ok");
    } else if (el.id === "lang-select" || el.id === "settings-lang") {
      const data = await run(el, () => api("/api/account/locale", { locale: el.value }));
      if (data) window.location.reload();
    } else if (el.id === "settings-tz") {
      const data = await run(el, () => api("/api/account/timezone", { timezone: el.value }));
      if (data) toast(t("saved"), "ok");
    } else if (el.matches("[data-master], [data-cat]")) {
      const master = {};
      document.querySelectorAll("[data-master]").forEach((box) => {
        master[box.dataset.master] = box.checked;
      });
      const prefs = {};
      document.querySelectorAll("[data-cat]").forEach((box) => {
        prefs[box.dataset.cat] = prefs[box.dataset.cat] || {};
        prefs[box.dataset.cat][box.dataset.ch] = box.checked;
      });
      const data = await run(null, () => api("/api/notifications/settings", { master, prefs }));
      if (data) {
        toast(t("saved"), "ok");
      } else {
        el.checked = !el.checked;
      }
    } else if (el.matches("[data-upload]")) {
      const file = el.files && el.files[0];
      if (!file) return;
      const data = await run(null, () => upload("/api/profile/" + (el.dataset.upload === "avatars" ? "avatar" : "banner"), file));
      el.value = "";
      if (!data) return;
      toast(t("saved"), "ok");
      window.location.reload();
    } else if (el.matches("[data-import]")) {
      const file = el.files && el.files[0];
      if (!file) return;
      toast(t("import_uploading"));
      const data = await run(el, () => upload("/api/import/tvtime", file));
      el.value = "";
      if (!data) return;
      toast(t("import_started"), "ok");
      pollImport();
    }
  });

  const importBox = document.getElementById("import-status");
  let importTimer = null;

  const renderImport = (job) => {
    if (!importBox || !job) return false;
    importBox.hidden = false;
    const line = document.getElementById("import-line");
    const bar = document.getElementById("import-bar");
    const details = document.getElementById("import-details");
    const list = document.getElementById("import-unmatched");
    const listTitle = document.getElementById("import-un-title");
    const pct = job.total > 0 ? Math.round((job.shows_done / job.total) * 100) : 0;
    bar.value = job.status === "done" ? 100 : pct;
    if (job.status === "pending") line.textContent = t("import_pending");
    else if (job.status === "running") line.textContent = fmt("import_running", job.shows_done, job.total);
    else if (job.status === "done") line.textContent = t("import_done");
    else line.textContent = t("import_failed");
    details.replaceChildren();
    const rows = [];
    if (job.status === "done" || job.status === "running") {
      rows.push(fmt("import_shows", job.matched, job.total));
      rows.push(fmt("import_follows", job.follows_added));
      rows.push(fmt("import_episodes", job.episodes_added));
      rows.push(fmt("import_skipped", job.episodes_skipped));
    }
    rows.forEach((text) => {
      const li = document.createElement("li");
      li.textContent = text;
      details.appendChild(li);
    });
    list.replaceChildren();
    const un = job.unmatched || [];
    listTitle.hidden = un.length === 0;
    un.forEach((item) => {
      const li = document.createElement("li");
      li.textContent = item.name + " (" + t("import_reason_" + item.reason) + ")";
      list.appendChild(li);
    });
    return job.active;
  };

  const pollImport = async () => {
    clearTimeout(importTimer);
    let data;
    try {
      data = await api("/api/import/status", null, "GET");
    } catch (err) {
      console.error("import status", err);
      importTimer = setTimeout(pollImport, 5000);
      return;
    }
    if (renderImport(data.job)) importTimer = setTimeout(pollImport, 2000);
  };

  if (importBox) pollImport();

  const dswitch = document.getElementById("dswitch");
  if (dswitch) {
    dswitch.checked = document.documentElement.getAttribute("data-theme") === "dark";
    const savedTheme = () => {
      try {
        return localStorage.getItem("tt-theme");
      } catch (err) {
        return null;
      }
    };
    if (window.matchMedia) {
      window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", (event) => {
        if (savedTheme()) return;
        document.documentElement.setAttribute("data-theme", event.matches ? "dark" : "light");
        dswitch.checked = event.matches;
      });
    }
    dswitch.addEventListener("change", () => {
      const next = dswitch.checked ? "dark" : "light";
      document.documentElement.setAttribute("data-theme", next);
      try {
        localStorage.setItem("tt-theme", next);
      } catch (err) {
        console.warn("theme not persisted", err);
      }
    });
  }

  const accountMenu = document.getElementById("account-menu");
  if (accountMenu) {
    document.addEventListener("click", (event) => {
      if (!accountMenu.contains(event.target)) accountMenu.open = false;
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") accountMenu.open = false;
    });
  }

  const SVG_NS = "http://www.w3.org/2000/svg";
  const icon = (id) => {
    const svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("class", "icon");
    svg.setAttribute("aria-hidden", "true");
    const use = document.createElementNS(SVG_NS, "use");
    use.setAttribute("href", "/static/icons.svg#" + id);
    svg.appendChild(use);
    return svg;
  };

  const pwRules = [
    ["pw_min", (v) => v.length >= 8],
    ["pw_upper", (v) => /\p{Lu}/u.test(v)],
    ["pw_lower", (v) => /\p{Ll}/u.test(v)],
    ["pw_digit", (v) => /\p{Nd}/u.test(v)],
    ["pw_special", (v) => /[!@#$%^&*()\-_+=[\]{}|;:'",.<>/?`~]/.test(v)],
  ];

  const setupPassword = (input) => {
    const wrap = document.createElement("span");
    wrap.className = "pw-wrap";
    input.parentNode.insertBefore(wrap, input);
    wrap.appendChild(input);
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "pw-toggle";
    btn.setAttribute("aria-label", t("pw_toggle"));
    let eye = icon("fa-eye");
    btn.appendChild(eye);
    btn.addEventListener("click", () => {
      const show = input.type === "password";
      input.type = show ? "text" : "password";
      const next = icon(show ? "fa-eye-slash" : "fa-eye");
      btn.replaceChild(next, eye);
      eye = next;
    });
    wrap.appendChild(btn);
    if (!("pwRules" in input.dataset)) return;
    const box = document.createElement("div");
    box.className = "pw-rules";
    box.hidden = true;
    const rows = pwRules.map(([key, test]) => {
      const row = document.createElement("div");
      row.appendChild(icon("fa-x-circle"));
      const text = document.createElement("span");
      text.textContent = t(key);
      row.appendChild(text);
      box.appendChild(row);
      return { row, test };
    });
    const strong = document.createElement("div");
    strong.className = "pw-strong";
    strong.hidden = true;
    strong.appendChild(icon("fa-check-circle"));
    const strongText = document.createElement("span");
    strongText.textContent = t("pw_strong");
    strong.appendChild(strongText);
    wrap.after(box);
    box.after(strong);
    input.addEventListener("input", () => {
      const v = input.value;
      box.hidden = v === "";
      let all = v !== "";
      rows.forEach(({ row, test }) => {
        const ok = test(v);
        if (!ok) all = false;
        row.classList.toggle("ok", ok);
        row.replaceChild(icon(ok ? "fa-check-circle" : "fa-x-circle"), row.firstChild);
      });
      strong.hidden = !all;
      box.hidden = all || v === "";
    });
  };

  document.querySelectorAll('input[type="password"]').forEach(setupPassword);

  const q = document.getElementById("q");
  const suggest = document.getElementById("suggest");
  if (q && suggest) {
    let timer = null;
    let seq = 0;
    const hide = () => {
      suggest.hidden = true;
      suggest.textContent = "";
    };
    q.addEventListener("input", () => {
      clearTimeout(timer);
      const value = q.value.trim();
      if (value.length < 2) return hide();
      timer = setTimeout(async () => {
        const mine = ++seq;
        let data;
        try {
          data = await api("/api/search?q=" + encodeURIComponent(value), null, "GET");
        } catch (err) {
          if (mine === seq) hide();
          console.warn("search failed", err);
          return;
        }
        if (mine !== seq) return;
        suggest.textContent = "";
        data.results.forEach((r) => {
          const a = document.createElement("a");
          a.href = "/show/" + r.id;
          const img = document.createElement("img");
          img.alt = "";
          img.src = r.poster_path ? "/img/w92" + r.poster_path : "/static/img/placeholder.svg";
          const span = document.createElement("span");
          span.textContent = r.name + (r.first_air_date ? " (" + r.first_air_date.slice(0, 4) + ")" : "");
          a.append(img, span);
          suggest.appendChild(a);
        });
        suggest.hidden = data.results.length === 0;
      }, 250);
    });
    document.addEventListener("click", (event) => {
      if (!event.target.closest(".search")) hide();
    });
    q.addEventListener("keydown", (event) => {
      if (event.key === "Escape") hide();
    });
  }

  const localTime = (stamp) => {
    const d = new Date(stamp.replace(" ", "T") + (stamp.includes("Z") ? "" : "Z"));
    return Number.isNaN(d.getTime()) ? stamp : d.toLocaleString(lang);
  };

  document.querySelectorAll("time[data-ts]").forEach((node) => {
    node.textContent = localTime(node.dataset.ts);
  });

  const bell = document.getElementById("bell");
  if (bell) {
    const badge = document.getElementById("bell-badge");
    const menu = document.getElementById("bell-menu");
    const list = document.getElementById("bell-list");
    const setUnread = (n) => {
      badge.textContent = n > 99 ? "99+" : String(n);
      badge.hidden = n <= 0;
    };
    const renderItems = (items) => {
      list.textContent = "";
      if (items.length === 0) {
        const li = document.createElement("li");
        li.className = "muted pad";
        li.textContent = t("notif_empty");
        list.appendChild(li);
        return;
      }
      items.forEach((n) => {
        const li = document.createElement("li");
        if (!n.read) li.className = "unread";
        const a = document.createElement("a");
        a.href = n.open;
        const title = document.createElement("strong");
        title.textContent = n.title;
        a.appendChild(title);
        if (n.body) {
          const body = document.createElement("span");
          body.textContent = n.body;
          a.appendChild(body);
        }
        const time = document.createElement("time");
        time.textContent = localTime(n.created_at);
        a.appendChild(time);
        li.appendChild(a);
        list.appendChild(li);
      });
    };
    const load = async () => {
      try {
        const data = await api("/api/notifications/recent", null, "GET");
        setUnread(data.unread);
        renderItems(data.items);
      } catch (err) {
        toast(err.message, "error");
      }
    };
    bell.addEventListener("click", () => {
      const open = menu.hidden;
      menu.hidden = !open;
      bell.setAttribute("aria-expanded", open ? "true" : "false");
      if (open) load();
    });
    document.addEventListener("click", (event) => {
      if (!event.target.closest(".bell-wrap")) {
        menu.hidden = true;
        bell.setAttribute("aria-expanded", "false");
      }
    });
    document.getElementById("bell-read-all").addEventListener("click", async () => {
      const data = await run(null, () => api("/api/notifications/read-all"));
      if (data) {
        setUnread(0);
        load();
      }
    });
    if (window.EventSource) {
      const source = new EventSource("/api/notifications/stream");
      source.addEventListener("notification", (event) => {
        let msg;
        try {
          msg = JSON.parse(event.data);
        } catch (err) {
          console.error("bad notification event", err);
          return;
        }
        setUnread(msg.unread_count);
        toast(msg.title + (msg.body ? " - " + msg.body : ""), "", msg.link || "/notifications");
        if (!menu.hidden) load();
      });
      source.onerror = () => console.warn("notification stream interrupted, browser will retry");
    }
    if ("serviceWorker" in navigator) {
      navigator.serviceWorker.addEventListener("message", (event) => {
        if (event.data && event.data.type === "tvt-badge" && typeof event.data.unreadCount === "number") setUnread(event.data.unreadCount);
      });
    }
  }

  const initComments = (root) => {
    const kind = root.dataset.kind;
    const target = root.dataset.target;
    const base = "/api/" + kind + "/" + target + "/comments";
    const logged = root.dataset.logged === "1";
    const verified = root.dataset.verified === "1";
    const admin = root.dataset.admin === "1";
    const list = document.getElementById("comment-list");
    const count = document.getElementById("comments-count");
    const more = document.getElementById("comments-more");
    const revealed = new Set();
    let viewerWatched = false;
    let nextOffset = 0;

    const button = (label, onClick, pressed) => {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = label;
      if (pressed !== undefined) b.setAttribute("aria-pressed", pressed ? "true" : "false");
      b.addEventListener("click", () => onClick(b));
      return b;
    };

    const load = async (append) => {
      let data;
      try {
        data = await api(base + "?offset=" + (append ? nextOffset : 0), null, "GET");
      } catch (err) {
        toast(err.message, "error");
        return;
      }
      viewerWatched = data.viewer_watched;
      count.textContent = "(" + data.total + ")";
      if (!append) list.textContent = "";
      if (data.comments.length === 0 && !append) {
        const li = document.createElement("li");
        li.className = "muted";
        li.textContent = t("comments_empty");
        list.appendChild(li);
      }
      data.comments.forEach((c) => list.appendChild(renderComment(c)));
      nextOffset = data.next_offset;
      more.hidden = !data.more;
    };

    const reload = () => load(false);

    more.addEventListener("click", async () => {
      more.disabled = true;
      await load(true);
      more.disabled = false;
    });

    const report = async (b, id, reportKind) => {
      let reason = "";
      if (reportKind === "abuse") {
        reason = window.prompt(t("report_reason"));
        if (reason === null) return false;
      }
      const data = await run(b, () => api("/api/comments/" + id + "/report", { kind: reportKind, reason }));
      if (data) {
        toast(t("reported"), "ok");
        return true;
      }
      return false;
    };

    const editForm = (c, bodyEl) => {
      const form = document.createElement("form");
      form.className = "comment-form";
      const ta = document.createElement("textarea");
      ta.value = c.body;
      ta.maxLength = 2000;
      ta.rows = 3;
      const row = document.createElement("div");
      row.className = "comment-form-row";
      const label = document.createElement("label");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.checked = c.spoiler;
      label.append(box, " " + t("mark_spoiler"));
      const save = document.createElement("button");
      save.type = "submit";
      save.className = "btn small";
      save.textContent = t("save");
      const cancel = button(t("cancel"), () => form.replaceWith(bodyEl));
      row.append(label, save, cancel);
      form.append(ta, row);
      form.addEventListener("submit", async (event) => {
        event.preventDefault();
        const data = await run(save, () => api("/api/comments/" + c.id + "/edit", { body: ta.value, spoiler: box.checked }));
        if (data) reload();
      });
      bodyEl.replaceWith(form);
      ta.focus();
    };

    const replyForm = (c, li) => {
      if (li.querySelector(".reply-form")) return;
      const form = document.createElement("form");
      form.className = "comment-form reply-form";
      const ta = document.createElement("textarea");
      ta.maxLength = 2000;
      ta.rows = 2;
      ta.required = true;
      const submit = document.createElement("button");
      submit.type = "submit";
      submit.className = "btn small";
      submit.textContent = t("post_reply");
      form.append(ta, submit);
      form.addEventListener("submit", async (event) => {
        event.preventDefault();
        const data = await run(submit, () => api(base, { body: ta.value, parent_id: c.id }));
        if (data) reload();
      });
      li.appendChild(form);
      ta.focus();
    };

    const renderComment = (c) => {
      const li = document.createElement("li");
      li.className = "comment" + (c.parent_id ? " reply" : "") + (c.deleted ? " deleted" : "");
      const head = document.createElement("div");
      head.className = "comment-head";
      if (c.deleted) {
        const who = document.createElement("strong");
        who.textContent = t("deleted_comment");
        head.appendChild(who);
      } else {
        const who = document.createElement("a");
        who.href = "/u/" + c.user;
        who.className = "comment-author";
        who.textContent = c.name;
        head.appendChild(who);
      }
      const time = document.createElement("time");
      const when = new Date(c.created_at.replace(" ", "T") + "Z");
      time.dateTime = when.toISOString();
      time.textContent = when.toLocaleString(lang);
      head.appendChild(time);
      if (c.edited && !c.deleted) {
        const tag = document.createElement("span");
        tag.className = "tag";
        tag.textContent = t("edited");
        head.appendChild(tag);
      }
      li.appendChild(head);
      if (c.deleted) return li;

      const body = document.createElement("div");
      body.className = "comment-body";
      body.textContent = c.body;
      const hidden = c.spoiler && !viewerWatched && !c.mine && !revealed.has(c.id);

      if (hidden) {
        const wrap = document.createElement("div");
        wrap.className = "spoiler-wrap";
        body.classList.add("spoiler");
        const cover = document.createElement("div");
        cover.className = "spoiler-cover";
        const label = document.createElement("span");
        label.className = "spoiler-label";
        label.textContent = t("spoiler_label");
        const show = button(t("show"), () => {
          revealed.add(c.id);
          wrap.replaceWith(body);
          body.classList.remove("spoiler");
          cover.remove();
        });
        show.className = "btn small";
        cover.append(label, show);
        if (logged) {
          const notSpoiler = button(t("report_not_spoiler"), async (b) => {
            const ok = await report(b, c.id, "not_spoiler");
            if (ok) {
              revealed.add(c.id);
              reload();
            }
          });
          notSpoiler.className = "btn small ghost";
          cover.appendChild(notSpoiler);
        }
        wrap.append(body, cover);
        li.appendChild(wrap);
      } else {
        li.appendChild(body);
        if (c.spoiler) {
          const tag = document.createElement("span");
          tag.className = "tag";
          tag.textContent = t("spoiler_tag");
          head.appendChild(tag);
        }
      }

      const actions = document.createElement("div");
      actions.className = "comment-actions";
      if (verified) actions.appendChild(button(t("reply"), () => replyForm(c, li)));
      if (c.mine) {
        actions.appendChild(button(t("edit"), () => editForm(c, body)));
        actions.appendChild(
          button(t("delete"), async (b) => {
            if (!window.confirm(t("delete_confirm"))) return;
            const data = await run(b, () => api("/api/comments/" + c.id + "/delete"));
            if (data) reload();
          })
        );
      } else if (logged) {
        const rep = c.reported || {};
        const reportKind = c.spoiler ? "not_spoiler" : "spoiler";
        actions.appendChild(
          button(
            c.spoiler ? t("report_not_spoiler") : t("report_spoiler"),
            async (b) => {
              if (await report(b, c.id, reportKind)) reload();
            },
            !!rep[reportKind]
          )
        );
        actions.appendChild(
          button(
            t("report_abuse"),
            async (b) => {
              if (await report(b, c.id, "abuse")) reload();
            },
            !!rep.abuse
          )
        );
        actions.appendChild(
          button(t("block"), async (b) => {
            if (!window.confirm(t("block_confirm"))) return;
            const data = await run(b, () => api("/api/comments/" + c.id + "/block"));
            if (data) reload();
          })
        );
      }
      if (admin && !c.mine) {
        actions.appendChild(
          button(t("admin_delete"), async (b) => {
            if (!window.confirm(t("delete_confirm"))) return;
            const data = await run(b, () => api("/api/admin/comments/" + c.id + "/delete"));
            if (data) reload();
          })
        );
      }
      li.appendChild(actions);
      return li;
    };

    const form = document.getElementById("comment-form");
    if (form) {
      form.addEventListener("submit", async (event) => {
        event.preventDefault();
        const submit = form.querySelector('[type="submit"]');
        const ta = form.querySelector("textarea");
        const spoiler = form.querySelector('[name="spoiler"]');
        const data = await run(submit, () => api(base, { body: ta.value, spoiler: spoiler.checked }));
        if (data) {
          form.reset();
          reload();
        }
      });
    }
    reload();
  };

  let telegramScript = null;
  const loadTelegram = () => {
    if (window.Telegram && window.Telegram.Login) return Promise.resolve();
    if (!telegramScript) {
      telegramScript = new Promise((resolve, reject) => {
        const s = document.createElement("script");
        s.src = "https://telegram.org/js/telegram-widget.js?22";
        s.onload = () => resolve();
        s.onerror = () => {
          telegramScript = null;
          reject(new Error(t("telegram_failed")));
        };
        document.head.appendChild(s);
      });
    }
    return telegramScript;
  };

  document.querySelectorAll("[data-telegram-btn]").forEach((btn) => {
    const label = btn.textContent;
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        await loadTelegram();
      } catch (err) {
        btn.disabled = false;
        toast(err.message, "error");
        return;
      }
      btn.disabled = false;
      let opened = false;
      const mark = () => {
        opened = true;
      };
      window.addEventListener("blur", mark, { once: true });
      document.addEventListener("visibilitychange", mark, { once: true });
      window.Telegram.Login.auth({ bot_id: btn.dataset.botId, request_access: "write" }, (data) => {
        if (!data || !data.hash) {
          btn.textContent = label;
          toast(t("telegram_failed"), "error");
          return;
        }
        const q = new URLSearchParams(data);
        if (btn.dataset.action === "link") q.set("action", "link");
        window.location.href = "/auth/telegram?" + q.toString();
      });
      setTimeout(() => {
        window.removeEventListener("blur", mark);
        document.removeEventListener("visibilitychange", mark);
        if (!opened) btn.textContent = t("telegram_open");
      }, 1500);
    });
  });

  const passkeysSupported = () =>
    !!(window.PublicKeyCredential && PublicKeyCredential.parseCreationOptionsFromJSON && PublicKeyCredential.parseRequestOptionsFromJSON);

  const passkeyError = (err) => (err && err.name === "NotAllowedError" ? t("passkey_cancelled") : err.message);

  const passkeyLogin = document.getElementById("passkey-login");
  if (passkeyLogin) {
    if (!passkeysSupported()) {
      passkeyLogin.disabled = true;
      passkeyLogin.textContent = t("passkey_unsupported");
    }
    passkeyLogin.addEventListener("click", async () => {
      passkeyLogin.disabled = true;
      try {
        const begin = await api("/api/auth/passkey/begin");
        const cred = await navigator.credentials.get({ publicKey: PublicKeyCredential.parseRequestOptionsFromJSON(begin.options.publicKey) });
        const done = await api("/api/auth/passkey/finish", cred.toJSON());
        go(done);
      } catch (err) {
        passkeyLogin.disabled = false;
        toast(passkeyError(err), "error");
      }
    });
  }

  const passkeyAdd = document.getElementById("passkey-add");
  if (passkeyAdd) {
    if (!passkeysSupported()) {
      passkeyAdd.disabled = true;
      passkeyAdd.textContent = t("passkey_unsupported");
    }
    passkeyAdd.addEventListener("click", async () => {
      passkeyAdd.disabled = true;
      const name = (document.getElementById("passkey-name") || {}).value || "";
      try {
        const begin = await api("/api/account/passkey/begin");
        const cred = await navigator.credentials.create({ publicKey: PublicKeyCredential.parseCreationOptionsFromJSON(begin.options.publicKey) });
        await api("/api/account/passkey/finish?name=" + encodeURIComponent(name.trim()), cred.toJSON());
        toast(t("passkey_added"), "ok");
        setTimeout(() => window.location.reload(), 700);
      } catch (err) {
        passkeyAdd.disabled = false;
        toast(passkeyError(err), "error");
      }
    });
  }

  const commentsRoot = document.getElementById("comments");
  if (commentsRoot) initComments(commentsRoot);

  const pushBox = document.getElementById("channels-box");
  if (pushBox && pushSupported()) {
    navigator.serviceWorker.ready
      .then((reg) => reg.pushManager.getSubscription())
      .then((sub) => {
        const on = document.getElementById("push-enable");
        const off = document.getElementById("push-disable");
        if (sub) {
          on.hidden = true;
          off.hidden = false;
        }
      })
      .catch((err) => console.warn("push state check failed", err));
  } else if (pushBox) {
    const on = document.getElementById("push-enable");
    if (on) on.disabled = true;
    const sub = document.getElementById("push-count");
    if (sub) sub.textContent = t("push_unsupported");
  }

  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js").catch((err) => console.warn("service worker registration failed", err));
  }
})();
