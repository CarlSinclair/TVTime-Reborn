(() => {
  const csrfMeta = document.querySelector('meta[name="csrf"]');
  let csrf = csrfMeta ? csrfMeta.content : "";
  const pageInits = [];
  const prefetched = new Map();
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
    if (method !== "GET") prefetched.clear();
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
    document.querySelectorAll("dialog[open]").forEach((dlg) => dlg.close());
    const dialog = document.getElementById("m-recovery");
    if (dialog) dialog.showModal();
  };

  document.addEventListener("click", (event) => {
    const opener = event.target.closest("[data-modal-open]");
    if (opener) {
      const dlg = document.getElementById(opener.dataset.modalOpen);
      if (dlg) dlg.showModal();
      return;
    }
    if (event.target.closest("[data-modal-close]")) {
      const dlg = event.target.closest("dialog");
      if (dlg) dlg.close();
      return;
    }
    if (event.target.tagName === "DIALOG") event.target.close();
  });

  document.addEventListener("submit", async (event) => {
    const form = event.target.closest("form[data-api]");
    if (!form) return;
    event.preventDefault();
    if (form.dataset.confirm && !(await confirmDialog(form.dataset.confirm))) return;
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
    document.dispatchEvent(new CustomEvent("tvt:form-done", { detail: form }));
    if (!data) return;
    const parentDialog = form.closest("dialog");
    if (parentDialog) parentDialog.close();
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

  const plainButton = (label, className, onClick) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = className;
    b.textContent = label;
    b.addEventListener("click", () => onClick(b));
    return b;
  };

  const confirmDialog = (message, label) =>
    new Promise((resolve) => {
      const dlg = document.createElement("dialog");
      dlg.className = "confirm-dialog modal";
      const text = document.createElement("p");
      text.className = "confirm-text";
      text.textContent = message;
      const row = document.createElement("div");
      row.className = "modal-actions";
      let answer = false;
      row.append(
        plainButton(t("cancel"), "btn ghost", () => dlg.close()),
        plainButton(label || t("confirm_ok"), "btn danger", () => {
          answer = true;
          dlg.close();
        })
      );
      dlg.append(text, row);
      dlg.addEventListener("close", () => {
        dlg.remove();
        resolve(answer);
      });
      document.body.appendChild(dlg);
      dlg.showModal();
    });

  const askUpto = (missing) =>
    new Promise((resolve) => {
      const dlg = document.createElement("dialog");
      dlg.className = "confirm-dialog modal";
      const title = document.createElement("h2");
      title.textContent = t("upto_title");
      const text = document.createElement("p");
      text.textContent = fmt("upto_text", missing);
      const row = document.createElement("div");
      row.className = "modal-actions";
      let answer = null;
      const choose = (value) => () => {
        answer = value;
        dlg.close();
      };
      row.append(plainButton(t("upto_no"), "btn ghost", choose(false)), plainButton(t("upto_yes"), "btn", choose(true)));
      dlg.append(title, text, row);
      dlg.addEventListener("close", () => {
        dlg.remove();
        resolve(answer);
      });
      document.body.appendChild(dlg);
      dlg.showModal();
    });

  const spriteURL = (() => {
    const own = document.querySelector('script[src*="/static/app.js"]');
    return "/static/icons.svg" + (own ? new URL(own.src, window.location.href).search : "");
  })();

  const icon = (name) => {
    const ns = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(ns, "svg");
    svg.setAttribute("class", "icon");
    svg.setAttribute("aria-hidden", "true");
    const use = document.createElementNS(ns, "use");
    use.setAttribute("href", spriteURL + "#" + name);
    svg.appendChild(use);
    return svg;
  };

  const stillsCache = new Map();
  const stillsFor = (episodeId) => {
    if (!episodeId) return Promise.resolve([]);
    if (!stillsCache.has(episodeId)) {
      stillsCache.set(
        episodeId,
        api("/api/episode/" + episodeId + "/stills", null, "GET")
          .then((data) => data.stills || [])
          .catch((err) => {
            console.error("episode stills", err);
            stillsCache.delete(episodeId);
            return [];
          })
      );
    }
    return stillsCache.get(episodeId);
  };

  const openStills = (stills, pick) => {
    const dlg = document.createElement("dialog");
    dlg.className = "stills-dialog modal";
    const title = document.createElement("h2");
    title.textContent = t("pic_stills_title");
    const grid = document.createElement("div");
    grid.className = "stills-grid";
    stills.forEach((file) => {
      const choice = document.createElement("button");
      choice.type = "button";
      choice.className = "still-choice";
      const im = document.createElement("img");
      im.loading = "lazy";
      im.alt = "";
      im.src = "/img/w300" + file;
      choice.appendChild(im);
      choice.addEventListener("click", () => {
        dlg.close();
        pick(file);
      });
      grid.appendChild(choice);
    });
    const row = document.createElement("div");
    row.className = "modal-actions";
    row.appendChild(plainButton(t("close"), "btn ghost", () => dlg.close()));
    dlg.append(title, grid, row);
    dlg.addEventListener("close", () => dlg.remove());
    document.body.appendChild(dlg);
    dlg.showModal();
  };

  let editorScript = null;
  const loadEditor = () => {
    if (window.TVTEditor) return Promise.resolve(window.TVTEditor);
    if (!editorScript) {
      editorScript = new Promise((resolve, reject) => {
        const s = document.createElement("script");
        const base = document.querySelector('script[src*="/static/app.js"]');
        s.src = "/static/editor.js" + (base ? new URL(base.src, window.location.href).search : "");
        s.onload = () => resolve(window.TVTEditor);
        s.onerror = () => {
          editorScript = null;
          reject(new Error(t("network_error")));
        };
        document.head.appendChild(s);
      });
    }
    return editorScript;
  };

  const picPicker = (textarea, episodeId) => {
    let name = "";
    const wrap = document.createElement("div");
    wrap.className = "pic-picker";
    const preview = document.createElement("div");
    preview.className = "pic-preview";
    preview.hidden = true;
    const img = document.createElement("img");
    img.alt = "";
    const bar = document.createElement("div");
    bar.className = "pic-bar";
    const file = document.createElement("input");
    file.type = "file";
    file.accept = "image/jpeg,image/png,image/gif,image/webp";
    file.hidden = true;
    const menu = document.createElement("div");
    menu.className = "pic-menu";
    menu.hidden = true;

    const set = (data) => {
      name = data ? data.name : "";
      img.src = data ? data.url : "";
      preview.hidden = !data;
      editBtn.hidden = !data || data.name.endsWith(".gif");
      bar.hidden = !!data;
      textarea.required = !data;
    };
    const discard = () => {
      const old = name;
      set(null);
      if (old) api("/api/pics/discard", { name: old }).catch((err) => console.error("discard picture", err));
    };
    const launch = async (button, options) => {
      const [editor, stills] = await run(button, () => Promise.all([loadEditor(), stillsFor(episodeId)])) || [];
      if (!editor) return;
      editor.open({
        t,
        api,
        upload,
        toast,
        icon,
        episodeId,
        hasStills: stills.length > 0,
        chooseStill: (pick) => openStills(stills, pick),
        stillUrl: (f) => "/api/episode/" + episodeId + "/still?file=" + encodeURIComponent(f),
        onDone: set,
        ...options,
      });
    };

    const photoBtn = document.createElement("button");
    photoBtn.type = "button";
    photoBtn.className = "icon-btn pic-btn";
    photoBtn.title = t("pic_add");
    photoBtn.setAttribute("aria-label", t("pic_add"));
    photoBtn.setAttribute("aria-haspopup", "true");
    photoBtn.appendChild(icon("fa-photo"));

    const item = (iconName, label, onClick) => {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "pic-menu-item";
      b.append(icon(iconName), document.createTextNode(" " + label));
      b.addEventListener("click", () => {
        menu.hidden = true;
        onClick();
      });
      return b;
    };
    const uploadItem = item("fa-upload", t("pic_upload"), () => file.click());
    const momentItem = item("fa-film", t("pic_moments"), async () => {
      const stills = await stillsFor(episodeId);
      openStills(stills, (f) => launch(photoBtn, { initialUrl: "/api/episode/" + episodeId + "/still?file=" + encodeURIComponent(f) }));
    });
    momentItem.hidden = true;
    menu.append(uploadItem, momentItem);

    const closeMenu = (event) => {
      if (event && wrap.contains(event.target)) return;
      menu.hidden = true;
      document.removeEventListener("click", closeMenu);
    };
    photoBtn.addEventListener("click", () => {
      menu.hidden = !menu.hidden;
      if (menu.hidden) return;
      setTimeout(() => document.addEventListener("click", closeMenu), 0);
      if (episodeId) stillsFor(episodeId).then((list) => (momentItem.hidden = list.length === 0));
    });
    if (episodeId) stillsFor(episodeId);

    file.addEventListener("change", async () => {
      const chosen = file.files[0];
      file.value = "";
      if (!chosen) return;
      if (chosen.type === "image/gif") {
        const data = await run(photoBtn, () => upload("/api/pics", chosen));
        if (data) set(data);
        return;
      }
      launch(photoBtn, { initialFile: chosen });
    });

    const editBtn = plainButton(t("ed_edit"), "btn small ghost", (b) => launch(b, { initialUrl: "/api/pics/source/" + name }));
    preview.append(img, editBtn, plainButton(t("pic_remove"), "btn small ghost", discard));
    bar.append(photoBtn, menu);
    wrap.append(preview, bar, file);
    return { el: wrap, value: () => name, reset: () => set(null) };
  };

  const syncSeasonCount = () => {
    const counter = document.querySelector("[data-season-count]");
    if (!counter) return;
    const done = document.querySelectorAll('.ep-card .watch-btn[data-state="1"]').length;
    counter.textContent = counter.dataset.tpl.replace("%d", done).replace("%d", counter.dataset.total);
  };

  const setWatchedState = (el, watched) => {
    el.dataset.state = watched ? "1" : "0";
    if (el.classList.contains("watch-btn")) {
      el.setAttribute("aria-pressed", watched ? "true" : "false");
    } else if (el.dataset.labelOn) {
      el.textContent = watched ? el.dataset.labelOn : el.dataset.labelOff;
    }
    syncSeasonCount();
  };

  const paintStars = (wrap, n) => {
    wrap.querySelectorAll(".star").forEach((star) => {
      star.classList.toggle("on", parseInt(star.dataset.star, 10) <= n);
    });
  };

  const markedMessage = () => {
    let first = true;
    try {
      first = !localStorage.getItem("tt-tip-rating");
      if (first) localStorage.setItem("tt-tip-rating", "1");
    } catch (err) {
      console.warn("tip state not stored", err);
    }
    return t(first ? "marked_tip" : "marked_watched");
  };

  const swapCard = (card, html) => {
    const list = card.parentNode;
    const finish = () => {
      if (html) {
        const holder = document.createElement("template");
        holder.innerHTML = html.trim();
        const fresh = holder.content.firstElementChild;
        if (fresh) card.replaceWith(fresh);
        else card.remove();
      } else {
        card.remove();
      }
      const empty = document.getElementById("upnext-empty");
      if (empty && list && list.id === "upnext") empty.hidden = list.children.length > 0;
    };
    card.classList.add("leaving");
    setTimeout(finish, 280);
  };

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

  const subscribePush = async () => {
    const perm = await Notification.requestPermission();
    if (perm !== "granted") throw new Error(t("push_denied"));
    const reg = await navigator.serviceWorker.ready;
    const key = (await api("/api/push/key", null, "GET")).key;
    const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlKey(key) });
    return api("/api/push/subscribe", sub.toJSON());
  };

  const unsubscribePush = async () => {
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.getSubscription();
    if (!sub) return { ok: true };
    const endpoint = sub.endpoint;
    await sub.unsubscribe();
    return api("/api/push/unsubscribe", { endpoint });
  };

  const saveNotifications = () => {
    const master = {};
    document.querySelectorAll("[data-master]").forEach((box) => {
      master[box.dataset.master] = box.checked;
    });
    const prefs = {};
    document.querySelectorAll("[data-cat]").forEach((box) => {
      prefs[box.dataset.cat] = prefs[box.dataset.cat] || {};
      prefs[box.dataset.cat][box.dataset.ch] = box.checked;
    });
    return api("/api/notifications/settings", { master, prefs });
  };

  const togglePush = async (el) => {
    if (el.checked && !pushSupported()) {
      el.checked = false;
      toast(t("push_unsupported"), "error");
      return;
    }
    const ok = await run(null, async () => {
      if (el.checked) await subscribePush();
      else await unsubscribePush();
      await saveNotifications();
      return true;
    });
    if (!ok) {
      el.checked = !el.checked;
      return;
    }
    toast(t("saved"), "ok");
    setTimeout(() => window.location.reload(), 600);
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

  const commentToggleValues = (row) => ({
    hide_comments: row.querySelector('[name="hide_comments"]').checked,
    delete_comments: row.querySelector('[name="delete_comments"]').checked,
    remove_ratings: !!(row.querySelector('[name="remove_ratings"]') || {}).checked,
  });

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
      const card = el.closest(".upnext-item");
      const url = "/api/episode/" + d.watch + "/watch";
      let data = await run(el, () => api(url, { watched, card: !!card }));
      if (!data) return;
      if (data.needs_confirm) {
        const all = await askUpto(data.missing);
        if (all === null) return;
        data = await run(el, () => api(url, all ? { upTo: true, card: !!card } : { watched: true, single: true, card: !!card }));
        if (!data) return;
        if (all && !card) {
          toast(fmt("marked_many", data.marked), "ok");
          window.location.reload();
          return;
        }
      }
      if (card) {
        swapCard(card, data.next_html);
        toast(markedMessage(), "ok");
        return;
      }
      setWatchedState(el, watched);
      return;
    }
    if ("friendAdd" in d || "friendAccept" in d || "friendDecline" in d || "friendRemove" in d) {
      const username = d.friendAdd || d.friendAccept || d.friendDecline || d.friendRemove;
      let call;
      if ("friendAdd" in d) call = () => api("/api/friends/request", { username });
      else if ("friendAccept" in d) call = () => api("/api/friends/respond", { username, accept: true });
      else if ("friendDecline" in d) call = () => api("/api/friends/respond", { username, accept: false });
      else {
        if (!(await confirmDialog(t("friend_remove_confirm")))) return;
        call = () => api("/api/friends/remove", { username });
      }
      const data = await run(el, call);
      if (!data) return;
      const box = el.closest("[data-friend-box]");
      const state = data.state || "none";
      if (box && box.classList.contains("notif-actions")) box.remove();
      else if (box) box.replaceChildren(...friendControls(username, state));
      if ("friendAdd" in d) toast(t(state === "friends" ? "friend_now" : "friend_sent"), "ok");
      if ("friendAccept" in d) toast(t("friend_now"), "ok");
      return;
    }
    if ("blockUser" in d) {
      if (!(await confirmDialog(t("block_confirm")))) return;
      const data = await run(el, () => api("/api/users/block", { username: d.blockUser }));
      if (data) {
        toast(t("user_blocked"), "ok");
        setTimeout(() => {
          window.location.href = "/";
        }, 700);
      }
      return;
    }
    if ("reportUser" in d) {
      const reason = window.prompt(t("report_user_reason"));
      if (reason === null) return;
      const data = await run(el, () => api("/api/users/report", { username: d.reportUser, reason }));
      if (data) toast(t("reported"), "ok");
      return;
    }
    if ("serverBlock" in d) {
      const data = await run(el, () => api("/api/admin/servers/block", { host: d.serverBlock, blocked: d.blocked === "1" }));
      if (data) window.location.reload();
      return;
    }
    if ("serverRemove" in d) {
      const dlg = document.getElementById("m-server-remove");
      dlg.dataset.host = d.serverRemove;
      dlg.dataset.mode = d.mode;
      dlg.querySelector("[data-remove-text]").textContent = d.confirm;
      dlg.querySelector("[data-server-remove-go]").textContent = d.label;
      dlg.querySelectorAll('input[type="checkbox"]').forEach((box) => {
        box.checked = false;
      });
      dlg.showModal();
      return;
    }
    if ("serverRemoveGo" in d) {
      const dlg = el.closest("dialog");
      const body = {
        host: dlg.dataset.host,
        drop_ratings: dlg.querySelector('[name="drop_ratings"]').checked,
        drop_comments: dlg.querySelector('[name="drop_comments"]').checked,
      };
      if (dlg.dataset.mode === "block") body.blocked = true;
      const data = await run(el, () => api(dlg.dataset.mode === "forget" ? "/api/admin/servers/forget" : "/api/admin/servers/block", body));
      if (data) window.location.reload();
      return;
    }
    if ("configTest" in d) {
      const form = el.closest("form");
      const body = formBody(form);
      body.group = d.configTest;
      const errBox = form.querySelector(".form-error");
      errBox.hidden = true;
      const data = await run(el, async () => {
        try {
          return await api("/api/admin/config/test", body);
        } catch (err) {
          errBox.textContent = err.message;
          errBox.hidden = false;
          throw err;
        }
      });
      if (data) toast(t("config_test_ok") + " " + data.detail, "ok");
      return;
    }
    if ("serverApprove" in d) {
      const data = await run(el, () => api("/api/admin/servers/share", { host: d.serverApprove, share: true }));
      if (data) window.location.reload();
      return;
    }
    if ("restart" in d) {
      if (!(await confirmDialog(d.confirm))) return;
      const data = await run(el, () => api("/api/admin/restart"));
      if (data) {
        toast(t("restarting"), "ok");
        setTimeout(() => window.location.reload(), 4000);
      }
      return;
    }
    if ("pagesAction" in d) {
      if (d.confirm && !(await confirmDialog(d.confirm))) return;
      const data = await run(el, () => api("/api/admin/pages", { action: d.pagesAction, page_id: d.pageId ? parseInt(d.pageId, 10) : 0 }));
      if (data) window.location.reload();
      return;
    }
    if ("logoRemove" in d) {
      const data = await run(el, () => api("/api/admin/logo/remove"));
      if (data) window.location.reload();
      return;
    }
    if ("userEdit" in d) {
      const form = document.getElementById("user-edit-form");
      form.dataset.api = "/api/admin/users/" + d.userEdit + "/update";
      form.dataset.userId = d.userEdit;
      form.querySelector('[name="username"]').value = d.username;
      form.querySelector('[name="display"]').value = d.display;
      form.querySelector('[name="email"]').value = d.email;
      form.querySelector('[name="verified"]').checked = d.verified === "1";
      const pw = form.querySelector('[name="password"]');
      pw.value = "";
      pw.dispatchEvent(new Event("input"));
      document.getElementById("user-edit-name").textContent = "@" + d.username;
      form.querySelector("[data-user-reset]").hidden = !d.email;
      const banBtn = form.querySelector("[data-user-ban]");
      const deactBtn = form.querySelector("[data-user-deactivate]");
      banBtn.dataset.state = d.banned;
      banBtn.textContent = d.banned === "1" ? banBtn.dataset.off : banBtn.dataset.on;
      deactBtn.dataset.state = d.deactivated;
      deactBtn.textContent = d.deactivated === "1" ? deactBtn.dataset.off : deactBtn.dataset.on;
      const locked = d.protected === "1";
      document.getElementById("user-edit-protected").hidden = !locked;
      document.querySelectorAll("#user-edit-status button, #user-edit-status input").forEach((ctl) => {
        ctl.disabled = locked;
      });
      form.querySelectorAll("[data-default]").forEach((box) => {
        box.checked = box.dataset.default === "1";
      });
      form.querySelector(".form-error").hidden = true;
      document.getElementById("m-user-edit").showModal();
      return;
    }
    if ("userReset" in d) {
      const id = el.closest("form").dataset.userId;
      const data = await run(el, () => api("/api/admin/users/" + id + "/reset-password"));
      if (data) toast(t("admin_reset_sent"), "ok");
      return;
    }
    if ("userBan" in d || "userDeactivate" in d) {
      if (el.dataset.state !== "1" && !(await confirmDialog(el.dataset.confirm))) return;
      const id = el.closest("form").dataset.userId;
      const on = el.dataset.state !== "1";
      const toggles = commentToggleValues(el.closest(".danger-row"));
      const call = "userBan" in d ? () => api("/api/admin/users/" + id + "/ban", Object.assign({ banned: on }, toggles)) : () => api("/api/admin/users/" + id + "/deactivate", Object.assign({ deactivated: on }, toggles));
      const data = await run(el, call);
      if (data) window.location.reload();
      return;
    }
    if ("userDeleteModal" in d) {
      if (!(await confirmDialog(d.confirm))) return;
      const id = el.closest("form").dataset.userId;
      const data = await run(el, () => api("/api/admin/users/" + id + "/delete", commentToggleValues(el.closest(".danger-row"))));
      if (data) window.location.reload();
      return;
    }
    if ("adminDismissUser" in d) {
      const data = await run(el, () => api("/api/admin/users/" + d.adminDismissUser + "/dismiss-reports"));
      if (data) window.location.reload();
      return;
    }
    if ("star" in d) {
      const wrap = el.closest(".stars");
      const card = el.closest(".upnext-item");
      const n = parseInt(d.star, 10);
      const current = parseInt(wrap.dataset.stars, 10) || 0;
      const stars = n === current ? 0 : n;
      paintStars(wrap, stars);
      const rateUrl = wrap.dataset.rateUrl || "/api/episode/" + wrap.dataset.ep + "/rate";
      const data = await run(null, () => api(rateUrl, { rating: stars * 2, card: !!card }));
      if (!data) {
        paintStars(wrap, current);
        return;
      }
      wrap.dataset.stars = String(stars);
      if (wrap.dataset.rateUrl) {
        toast(t("saved"), "ok");
        return;
      }
      if (card) {
        setTimeout(() => swapCard(card, data.next_html), 350);
        toast(t("rated"), "ok");
        return;
      }
      if (data.watched) {
        document.querySelectorAll('[data-watch="' + wrap.dataset.ep + '"]').forEach((btn) => setWatchedState(btn, true));
        toast(t("rated"), "ok");
      } else {
        toast(t("saved"), "ok");
      }
      return;
    }
    if ("seasonWatch" in d) {
      const box = el.closest("[data-season]");
      const data = await run(el, () =>
        api("/api/show/" + box.dataset.tmdb + "/season/" + box.dataset.season + "/watch", { watched: d.seasonWatch === "1" })
      );
      if (data) {
        const on = d.seasonWatch === "1";
        document.querySelectorAll("[data-watch]").forEach((btn) => setWatchedState(btn, on));
        toast(fmt("marked_many", data.marked), "ok");
      }
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
      if (!(await confirmDialog(t("delete_confirm"), t("delete")))) return;
      const data = await run(el, () => api("/api/admin/comments/" + d.adminDelete + "/delete"));
      if (data) window.location.reload();
      return;
    }
    if ("adminBan" in d) {
      if (!(await confirmDialog(t("ban_confirm")))) return;
      const data = await run(el, () => api("/api/admin/users/" + d.adminBan + "/ban", { banned: true }));
      if (data) window.location.reload();
      return;
    }
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
      if (!(await confirmDialog(t("telegram_unlink_confirm")))) return;
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
      document.getElementById("m-twofa").showModal();
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
      if (!(await confirmDialog(t("passkey_remove_confirm")))) return;
      const data = await run(el, () => api("/api/account/passkey/" + d.passkeyDelete + "/delete"));
      if (data) window.location.reload();
      return;
    }
    if ("removeMedia" in d) {
      const data = await run(el, () => api("/api/profile/" + (d.removeMedia === "avatars" ? "avatar" : "banner") + "/remove"));
      if (data) window.location.reload();
    }
  });

  pageInits.push(() => {
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
        if (data) showRecovery(data.recovery_codes);
      });
    }
  });

  document.addEventListener("change", async (event) => {
    const el = event.target;
    if (el.matches("[data-follow-status]")) {
      const data = await run(el, () => api("/api/show/" + tmdbOf(el) + "/follow", { status: el.value }));
      if (data) toast(t("saved"), "ok");
    } else if (el.id === "lang-select" || el.id === "settings-lang") {
      const data = await run(el, () => api("/api/account/locale", { locale: el.value }));
      if (data) window.location.reload();
    } else if (el.matches('[name="hide_comments"], [name="delete_comments"]')) {
      if (el.checked) {
        const other = el.closest(".comment-toggles").querySelector(el.name === "hide_comments" ? '[name="delete_comments"]' : '[name="hide_comments"]');
        other.checked = false;
      }
    } else if (el.id === "federation-switch") {
      const data = await run(el, () => api("/api/admin/config", { group: "federation", federation: el.checked }));
      if (data) window.location.reload();
      else el.checked = !el.checked;
    } else if (el.id === "signups-switch") {
      const data = await run(el, () => api("/api/admin/signups", { closed: !el.checked }));
      if (data) toast(t("saved"), "ok");
      else el.checked = !el.checked;
    } else if (el.id === "settings-tz") {
      const data = await run(el, () => api("/api/account/timezone", { timezone: el.value }));
      if (data) toast(t("saved"), "ok");
    } else if (el.id === "ch-push") {
      togglePush(el);
    } else if (el.matches("[data-master], [data-cat]")) {
      const data = await run(null, saveNotifications);
      if (data) {
        toast(t("saved"), "ok");
      } else {
        el.checked = !el.checked;
      }
    } else if (el.matches("[data-upload]")) {
      const file = el.files && el.files[0];
      if (!file) return;
      const target = el.dataset.upload === "logo" ? "/api/admin/logo" : "/api/profile/" + (el.dataset.upload === "avatars" ? "avatar" : "banner");
      const data = await run(null, () => upload(target, file));
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
      if (!data) {
        pollImport();
        return;
      }
      toast(t("import_started"), "ok");
      pollImport();
    }
  });

  let importBox = null;
  let importTimer = null;

  const renderImport = (job) => {
    if (!importBox || !job) return false;
    importBox.hidden = false;
    const line = document.getElementById("import-line");
    const bar = document.getElementById("import-bar");
    const details = document.getElementById("import-details");
    const list = document.getElementById("import-unmatched");
    const listTitle = document.getElementById("import-un-title");
    const picker = document.getElementById("import-picker");
    if (picker) picker.hidden = job.active;
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

  pageInits.push(() => {
    clearTimeout(importTimer);
    importBox = document.getElementById("import-status");
    if (importBox) pollImport();
  });

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
      if (!accountMenu.contains(event.target) || event.target.closest(".menu a, .menu button")) accountMenu.open = false;
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") accountMenu.open = false;
    });
  }

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

  pageInits.push(() => document.querySelectorAll('input[type="password"]').forEach(setupPassword));

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
        const people = value.startsWith("@");
        let data;
        try {
          data = await api((people ? "/api/people?q=" : "/api/search?q=") + encodeURIComponent(value), null, "GET");
        } catch (err) {
          if (mine === seq) hide();
          console.warn("search failed", err);
          return;
        }
        if (mine !== seq) return;
        suggest.textContent = "";
        if (people) {
          data.results.forEach((r) => {
            const a = document.createElement("a");
            a.href = "/u/" + r.username;
            const span = document.createElement("span");
            span.textContent = r.name + " @" + r.username;
            if (r.avatar) {
              const img = document.createElement("img");
              img.alt = "";
              img.src = r.avatar;
              a.append(img);
            }
            a.append(span);
            suggest.appendChild(a);
          });
          suggest.hidden = data.results.length === 0;
          return;
        }
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

  const localizeTimes = (root) => {
    (root || document).querySelectorAll("time[data-ts]").forEach((node) => {
      node.textContent = localTime(node.dataset.ts);
    });
  };
  pageInits.push(() => localizeTimes());
  pageInits.push(() => {
    if (window.tvtCaptcha) window.tvtCaptcha.bindAll();
  });

  const iconButton = (iconId, cls, label, attr, value) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "icon-btn " + cls;
    b.setAttribute("aria-label", label);
    b.title = label;
    if (attr) b.setAttribute(attr, value);
    b.appendChild(icon(iconId));
    return b;
  };

  const friendControls = (username, state) => {
    if (state === "none") return [iconButton("fa-user-plus", "solid", t("friend_add"), "data-friend-add", username)];
    if (state === "incoming") {
      return [
        iconButton("fa-check", "ok", t("friend_accept"), "data-friend-accept", username),
        iconButton("fa-x", "no", t("friend_decline"), "data-friend-decline", username),
      ];
    }
    if (state === "outgoing") {
      const tag = document.createElement("span");
      tag.className = "tag pending";
      tag.textContent = t("friend_pending");
      return [tag];
    }
    return [iconButton("fa-x", "no", t("friend_remove"), "data-friend-remove", username)];
  };

  const actionButton = (iconId, cls, label, handler) => {
    const b = iconButton(iconId, cls, label);
    b.addEventListener("click", handler);
    return b;
  };

  const friendsDialog = document.getElementById("m-friends");

  const renderPeople = (ul, people, actions, append) => {
    if (!append) ul.replaceChildren();
    people.forEach((p) => {
      const li = document.createElement("li");
      const link = document.createElement("a");
      link.href = "/u/" + p.username;
      link.textContent = p.name + " (@" + p.username + ")";
      const box = document.createElement("span");
      box.className = "row-actions";
      actions(p).forEach((b) => box.appendChild(b));
      li.append(link, box);
      ul.appendChild(li);
    });
  };

  const friendLists = {
    friends: { ul: "friends-list", more: "friends-more", total: 0 },
    incoming: { ul: "friends-incoming", more: "friends-incoming-more", total: 0 },
    outgoing: { ul: "friends-outgoing", more: "friends-outgoing-more", total: 0 },
  };

  const friendActions = (kind, act) => {
    if (kind === "incoming") {
      return (p) => [
        actionButton("fa-check", "ok", t("friend_accept"), () => act("/api/friends/respond", { username: p.username, accept: true }, t("friend_now"))),
        actionButton("fa-x", "no", t("friend_decline"), () => act("/api/friends/respond", { username: p.username, accept: false })),
      ];
    }
    if (kind === "outgoing") return (p) => [actionButton("fa-x", "no", t("friend_cancel"), () => act("/api/friends/remove", { username: p.username }))];
    return (p) => [
      actionButton("fa-x", "no", t("friend_remove"), async () => {
        if (await confirmDialog(t("friend_remove_confirm"))) act("/api/friends/remove", { username: p.username });
      }),
    ];
  };

  const showFriends = (data, kinds, append) => {
    const act = async (path, body, message) => {
      const res = await run(null, () => api(path, body));
      if (!res) return;
      if (message) toast(message, "ok");
      await loadFriends();
    };
    kinds.forEach((kind) => {
      const list = friendLists[kind];
      renderPeople(document.getElementById(list.ul), data[kind], friendActions(kind, act), append);
      list.total = append ? list.total + data[kind].length : data[kind].length;
      document.getElementById(list.more).hidden = !data[kind + "_more"];
    });
  };

  const loadFriends = async () => {
    const data = await api("/api/friends", null, "GET");
    showFriends(data, ["friends", "incoming", "outgoing"], false);
    document.getElementById("friends-incoming-box").hidden = data.incoming.length === 0;
    document.getElementById("friends-outgoing-box").hidden = data.outgoing.length === 0;
    document.getElementById("friends-none").hidden = data.friends.length + data.incoming.length + data.outgoing.length > 0;
    const badge = document.getElementById("friends-badge");
    if (badge) {
      badge.textContent = String(data.incoming_total);
      badge.hidden = data.incoming_total === 0;
    }
  };

  const loadMoreFriends = async (kind) => {
    const data = await api("/api/friends?" + kind + "_offset=" + friendLists[kind].total, null, "GET");
    showFriends(data, [kind], true);
  };

  document.addEventListener("click", (event) => {
    const more = event.target.closest("[data-friends-more]");
    if (!more) return;
    run(more, () => loadMoreFriends(more.dataset.friendsMore));
  });

  const openFriends = async () => {
    if (!friendsDialog) return;
    const menu = document.getElementById("account-menu");
    if (menu) menu.open = false;
    const ok = await run(null, async () => {
      await loadFriends();
      return true;
    });
    if (ok) friendsDialog.showModal();
  };

  document.addEventListener("click", (event) => {
    if (event.target.closest("[data-friends-open]")) openFriends();
  });

  const friendsForm = document.getElementById("friends-add-form");
  if (friendsForm) {
    friendsForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const input = document.getElementById("friends-add-name");
      const errBox = document.getElementById("friends-error");
      errBox.hidden = true;
      const name = input.value.trim();
      if (!name) return;
      const data = await run(null, async () => {
        try {
          return await api("/api/friends/request", { username: name });
        } catch (err) {
          errBox.textContent = err.message;
          errBox.hidden = false;
          throw err;
        }
      });
      if (!data) return;
      input.value = "";
      toast(t(data.state === "friends" ? "friend_now" : "friend_sent"), "ok");
      await loadFriends();
    });
  }

  pageInits.push(() => {
    if (new URLSearchParams(window.location.search).get("friends") === "1") openFriends();
  });

  pageInits.push(() => {
    const homeTabs = document.querySelectorAll("[data-home-tab]");
    if (homeTabs.length) {
      const upcoming = document.getElementById("home-upcoming");
      const feed = document.getElementById("home-feed");
      const list = document.getElementById("feed-list");
      const note = document.getElementById("feed-note");
      const more = document.getElementById("feed-more");
      let feedLoaded = false;
      let cursor = "";
      const loadFeed = async (before) => {
        const data = await run(null, () => api("/api/feed" + (before ? "?before=" + encodeURIComponent(before) : ""), null, "GET"));
        if (!data) return;
        list.insertAdjacentHTML("beforeend", data.html);
        localizeTimes(list);
        if (!before) {
          note.hidden = data.mode === "friends";
          note.textContent = t(data.mode === "suggested" ? "feed_no_friends" : "feed_none");
        }
        cursor = data.next || "";
        more.hidden = !cursor;
      };
      const show = (name) => {
        homeTabs.forEach((b) => b.classList.toggle("active", b.dataset.homeTab === name));
        upcoming.hidden = name !== "upcoming";
        feed.hidden = name !== "feed";
        if (name === "feed" && !feedLoaded) {
          feedLoaded = true;
          loadFeed("");
        }
        try {
          localStorage.setItem("tt-home-tab", name);
        } catch (err) {
          console.warn("home tab not stored", err);
        }
      };
      homeTabs.forEach((b) => b.addEventListener("click", () => show(b.dataset.homeTab)));
      more.addEventListener("click", () => loadFeed(cursor));
      let start = new URLSearchParams(window.location.search).get("tab");
      if (!start) {
        try {
          start = localStorage.getItem("tt-home-tab");
        } catch (err) {
          start = null;
        }
      }
      if (start === "feed") show("feed");
    }
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
        if (n.friend) {
          const actions = document.createElement("span");
          actions.className = "notif-actions";
          actions.setAttribute("data-friend-box", "");
          friendControls(n.friend, "incoming").forEach((b) => actions.appendChild(b));
          li.appendChild(actions);
        }
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
    let oldestTop = "";

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
        data = await api(base + "?offset=" + (append ? nextOffset : 0) + (append && oldestTop ? "&before=" + encodeURIComponent(oldestTop) : ""), null, "GET");
      } catch (err) {
        toast(err.message, "error");
        return;
      }
      viewerWatched = data.viewer_watched;
      count.textContent = "(" + data.total + ")";
      if (!append) list.textContent = "";
      if (kind === "profile") root.hidden = data.total === 0 && !document.getElementById("comment-form");
      if (data.comments.length === 0 && !append && kind !== "profile") {
        const li = document.createElement("li");
        li.className = "muted";
        li.textContent = t("comments_empty");
        list.appendChild(li);
      }
      data.comments.forEach((c) => list.appendChild(renderComment(c)));
      data.comments.forEach((c) => {
        if (!c.parent_id) oldestTop = c.created_at;
      });
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
      cancel.className = "btn small ghost";
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
      const pic = picPicker(ta, kind === "episode" ? target : 0);
      form.append(ta, pic.el, submit);
      form.addEventListener("submit", async (event) => {
        event.preventDefault();
        const data = await run(submit, () => api(base, { body: ta.value, parent_id: c.id, image: pic.value() }));
        if (data) reload();
      });
      li.appendChild(form);
      ta.focus();
    };

    const renderComment = (c) => {
      const li = document.createElement("li");
      const gone = c.deleted || c.hidden;
      li.className = "comment" + (c.parent_id ? " reply" : "") + (gone ? " deleted" : "");
      const head = document.createElement("div");
      head.className = "comment-head";
      if (gone || c.user === "deleted") {
        const who = document.createElement("strong");
        who.textContent = c.hidden ? t("hidden_comment") : gone ? t("deleted_comment") : t("anonymous");
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
      if (c.edited && !gone) {
        const tag = document.createElement("span");
        tag.className = "tag";
        tag.textContent = t("edited");
        head.appendChild(tag);
      }
      li.appendChild(head);
      if (gone) return li;

      const body = document.createElement("div");
      body.className = "comment-body";
      body.textContent = c.body;
      if (c.image) {
        const link = document.createElement("a");
        link.href = c.image;
        link.target = "_blank";
        link.rel = "noopener";
        const pic = document.createElement("img");
        pic.className = "comment-img";
        pic.loading = "lazy";
        pic.alt = "";
        pic.src = c.image;
        pic.addEventListener("error", () => link.remove());
        link.appendChild(pic);
        body.appendChild(link);
      }
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
        if (logged && !c.host) {
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
      if (c.host) {
        if (logged) {
          actions.appendChild(
            button(t("report_abuse"), async (b) => {
              const reason = window.prompt(t("report_reason"));
              if (reason === null) return;
              const data = await run(b, () => api("/api/remote-comments/" + -c.id + "/report", { reason }));
              if (data) toast(t("reported"), "ok");
            })
          );
          actions.appendChild(
            button(t("block"), async (b) => {
              if (!(await confirmDialog(t("block_confirm")))) return;
              const data = await run(b, () => api("/api/users/block", { username: c.user }));
              if (data) reload();
            })
          );
        }
      } else if (c.mine) {
        actions.appendChild(button(t("edit"), () => editForm(c, body)));
        actions.appendChild(
          button(t("delete"), async (b) => {
            if (!(await confirmDialog(t("delete_confirm"), t("delete")))) return;
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
            if (!(await confirmDialog(t("block_confirm")))) return;
            const data = await run(b, () => api("/api/comments/" + c.id + "/block"));
            if (data) reload();
          })
        );
      }
      if (admin && !c.mine && !c.host) {
        actions.appendChild(
          button(t("admin_delete"), async (b) => {
            if (!(await confirmDialog(t("delete_confirm"), t("delete")))) return;
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
      const ta = form.querySelector("textarea");
      const pic = picPicker(ta, kind === "episode" ? target : 0);
      ta.insertAdjacentElement("afterend", pic.el);
      form.addEventListener("submit", async (event) => {
        event.preventDefault();
        const submit = form.querySelector('[type="submit"]');
        const spoiler = form.querySelector('[name="spoiler"]');
        const data = await run(submit, () => api(base, { body: ta.value, spoiler: spoiler.checked, image: pic.value() }));
        if (data) {
          form.reset();
          pic.reset();
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

  pageInits.push(() => {
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
  });

  const passkeysSupported = () =>
    !!(window.PublicKeyCredential && PublicKeyCredential.parseCreationOptionsFromJSON && PublicKeyCredential.parseRequestOptionsFromJSON);

  const passkeyError = (err) => (err && err.name === "NotAllowedError" ? t("passkey_cancelled") : err.message);

  pageInits.push(() => {
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
  });

  const runPageInits = () => pageInits.forEach((fn) => fn());

  const plainPaths = ["/api/", "/static/", "/media/", "/img/", "/tvdbimg/", "/auth/", "/login", "/register", "/forgot", "/t/", "/federation", "/.well-known", "/sw.js", "/manifest", "/healthz"];
  const softPath = (path) => !plainPaths.some((p) => path === p || path.startsWith(p.endsWith("/") ? p : p + "/") || (p === "/login" && path.startsWith("/login")));

  const softLink = (a, event) => {
    if (event && (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey)) return false;
    if (!a || !a.href || (a.target && a.target !== "_self") || a.hasAttribute("download") || "noAjax" in a.dataset) return false;
    const url = new URL(a.href, window.location.href);
    if (url.origin !== window.location.origin || !softPath(url.pathname)) return false;
    if (url.pathname === window.location.pathname && url.search === window.location.search && url.hash) return false;
    return true;
  };

  let navProgress = null;
  const progress = (on) => {
    if (!navProgress) {
      navProgress = document.createElement("div");
      navProgress.className = "nav-progress";
      navProgress.setAttribute("aria-hidden", "true");
      document.body.appendChild(navProgress);
    }
    navProgress.classList.toggle("on", on);
  };

  const fetchPage = (url, signal) =>
    fetch(url, { credentials: "same-origin", headers: { "X-Requested-With": "tvt-nav" }, signal }).then(async (res) => ({
      url: res.url,
      type: res.headers.get("Content-Type") || "",
      html: await res.text(),
    }));

  const prefetch = (url) => {
    const hit = prefetched.get(url);
    if (hit && Date.now() - hit.at < 8000) return;
    if (prefetched.size > 20) prefetched.clear();
    const entry = { at: Date.now(), page: fetchPage(url) };
    entry.page.catch(() => prefetched.delete(url));
    prefetched.set(url, entry);
  };

  let navCtl = null;
  let here = window.location.pathname + window.location.search;
  if ("scrollRestoration" in history) history.scrollRestoration = "manual";

  const syncChrome = (doc) => {
    const fresh = doc.querySelector('meta[name="csrf"]');
    if (fresh && fresh.content) {
      csrf = fresh.content;
      if (csrfMeta) csrfMeta.content = fresh.content;
    }
    ["nav.nav", "nav.bottomnav"].forEach((sel) => {
      const next = doc.querySelector(sel);
      const now = document.querySelector(sel);
      if (!next || !now) return;
      now.querySelectorAll("a").forEach((a) => {
        const match = Array.from(next.querySelectorAll("a")).find((b) => b.getAttribute("href") === a.getAttribute("href"));
        if (match && match.hasAttribute("aria-current")) a.setAttribute("aria-current", "page");
        else a.removeAttribute("aria-current");
      });
    });
    const field = document.getElementById("q");
    const nextField = doc.getElementById("q");
    if (field && nextField) field.value = nextField.value;
  };

  const navigate = async (url, opts) => {
    const push = !opts || opts.push !== false;
    if (navCtl) navCtl.abort();
    navCtl = new AbortController();
    const ctl = navCtl;
    progress(true);
    let page;
    try {
      const hit = prefetched.get(url);
      page = hit && Date.now() - hit.at < 8000 ? await hit.page : await fetchPage(url, ctl.signal);
      if (ctl.signal.aborted) return;
    } catch (err) {
      if (err.name === "AbortError") return;
      console.warn("soft navigation failed, loading normally", url, err);
      window.location.href = url;
      return;
    }
    const final = new URL(page.url, window.location.href);
    const doc = page.type.includes("text/html") ? new DOMParser().parseFromString(page.html, "text/html") : null;
    const incoming = doc && doc.querySelector("main.container");
    const currentVersion = document.querySelector('meta[name="app-version"]');
    const nextVersion = doc && doc.querySelector('meta[name="app-version"]');
    const stale = currentVersion && nextVersion && currentVersion.content !== nextVersion.content;
    if (!incoming || stale || !softPath(final.pathname)) {
      window.location.href = url;
      return;
    }
    try {
      history.replaceState(Object.assign({}, history.state, { y: window.scrollY }), "");
    } catch (err) {
      console.warn("scroll position not saved", err);
    }
    if (push) history.pushState({}, "", final.pathname + final.search + final.hash);
    here = final.pathname + final.search;
    document.querySelectorAll("dialog[open]").forEach((dlg) => dlg.close());
    const menu = document.getElementById("account-menu");
    if (menu) menu.open = false;
    const bellMenu = document.getElementById("bell-menu");
    if (bellMenu) bellMenu.hidden = true;
    clearInterval(tgPoll);
    document.title = doc.title;
    const main = document.querySelector("main.container");
    main.replaceChildren(...Array.from(incoming.childNodes).map((n) => document.adoptNode(n)));
    syncChrome(doc);
    runPageInits();
    progress(false);
    const y = opts && typeof opts.y === "number" ? opts.y : 0;
    if (final.hash && document.getElementById(decodeURIComponent(final.hash.slice(1)))) {
      document.getElementById(decodeURIComponent(final.hash.slice(1))).scrollIntoView();
    } else {
      window.scrollTo(0, y);
    }
    main.setAttribute("tabindex", "-1");
    main.focus({ preventScroll: true });
  };

  document.addEventListener("click", (event) => {
    const a = event.target.closest("a[href]");
    if (!softLink(a, event)) return;
    event.preventDefault();
    navigate(a.href).catch((err) => {
      console.error("navigation", err);
      window.location.href = a.href;
    });
  });

  let hoverTimer = null;
  const warm = (event) => {
    const a = event.target.closest && event.target.closest("a[href]");
    if (!softLink(a, null)) return;
    clearTimeout(hoverTimer);
    hoverTimer = setTimeout(() => prefetch(a.href), event.type === "touchstart" ? 0 : 80);
  };
  document.addEventListener("mouseover", warm);
  document.addEventListener("touchstart", warm, { passive: true });

  document.addEventListener("submit", (event) => {
    const form = event.target;
    if (event.defaultPrevented || !(form instanceof HTMLFormElement) || form.hasAttribute("data-api") || (form.method || "get").toLowerCase() !== "get" || "noAjax" in form.dataset) return;
    const url = new URL(form.getAttribute("action") || window.location.pathname, window.location.href);
    if (url.origin !== window.location.origin || !softPath(url.pathname)) return;
    const params = new URLSearchParams(new FormData(form));
    url.search = params.toString();
    event.preventDefault();
    navigate(url.toString()).catch(() => {
      window.location.href = url.toString();
    });
  });

  window.addEventListener("popstate", (event) => {
    const now = window.location.pathname + window.location.search;
    if (now === here) return;
    navigate(window.location.href, { push: false, y: event.state && event.state.y }).catch(() => window.location.reload());
  });

  runPageInits();

  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/sw.js").catch((err) => console.warn("service worker registration failed", err));
  }
})();
