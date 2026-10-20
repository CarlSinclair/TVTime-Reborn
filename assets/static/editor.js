(() => {
  const LAYOUTS = {
    single: [[0, 0, 1, 1]],
    h2: [
      [0, 0, 0.5, 1],
      [0.5, 0, 0.5, 1],
    ],
    v2: [
      [0, 0, 1, 0.5],
      [0, 0.5, 1, 0.5],
    ],
    bigRight: [
      [0, 0, 0.6, 1],
      [0.6, 0, 0.4, 0.5],
      [0.6, 0.5, 0.4, 0.5],
    ],
    bigBottom: [
      [0, 0, 1, 0.6],
      [0, 0.6, 0.5, 0.4],
      [0.5, 0.6, 0.5, 0.4],
    ],
    grid4: [
      [0, 0, 0.5, 0.5],
      [0.5, 0, 0.5, 0.5],
      [0, 0.5, 0.5, 0.5],
      [0.5, 0.5, 0.5, 0.5],
    ],
    h3: [
      [0, 0, 1 / 3, 1],
      [1 / 3, 0, 1 / 3, 1],
      [2 / 3, 0, 1 / 3, 1],
    ],
    v3: [
      [0, 0, 1, 1 / 3],
      [0, 1 / 3, 1, 1 / 3],
      [0, 2 / 3, 1, 1 / 3],
    ],
  };
  const ASPECTS = { auto: 0, "16:9": 16 / 9, "4:3": 4 / 3, "1:1": 1, "4:5": 4 / 5, "9:16": 9 / 16 };
  const WIDTH = 1280;
  const MAX_SIDE = 1920;
  const FONT = '"Impact", "Anton", "Arial Black", "Helvetica Neue", Arial, sans-serif';

  const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));

  const outputSize = (aspect, firstW, firstH, layout) => {
    let ratio = ASPECTS[aspect];
    if (!ratio) ratio = layout === "single" && firstW > 0 && firstH > 0 ? firstW / firstH : 16 / 9;
    let w = WIDTH;
    let h = Math.round(w / ratio);
    if (h > MAX_SIDE) {
      h = MAX_SIDE;
      w = Math.round(h * ratio);
    }
    return { w, h };
  };

  const cellRects = (layout, w, h, gap) =>
    (LAYOUTS[layout] || LAYOUTS.single).map(([x, y, cw, ch]) => ({
      x: Math.round(x * w + gap / 2),
      y: Math.round(y * h + gap / 2),
      w: Math.max(1, Math.round(cw * w - gap)),
      h: Math.max(1, Math.round(ch * h - gap)),
    }));

  const coverRect = (bw, bh, cell, zoom, ox, oy) => {
    const scale = Math.max(cell.w / bw, cell.h / bh) * zoom;
    const dw = bw * scale;
    const dh = bh * scale;
    const slackX = Math.max(0, (dw - cell.w) / 2);
    const slackY = Math.max(0, (dh - cell.h) / 2);
    return {
      x: cell.x + (cell.w - dw) / 2 + clamp(ox, -1, 1) * slackX,
      y: cell.y + (cell.h - dh) / 2 + clamp(oy, -1, 1) * slackY,
      w: dw,
      h: dh,
      slackX,
      slackY,
    };
  };

  const wrapLines = (measure, text, maxWidth) => {
    const lines = [];
    String(text)
      .split("\n")
      .forEach((paragraph) => {
        const words = paragraph.split(/\s+/).filter(Boolean);
        if (words.length === 0) {
          lines.push("");
          return;
        }
        let line = "";
        const push = () => {
          if (line) lines.push(line);
          line = "";
        };
        words.forEach((word) => {
          let rest = word;
          while (measure(rest) > maxWidth && rest.length > 1) {
            let cut = rest.length - 1;
            while (cut > 1 && measure(rest.slice(0, cut)) > maxWidth) cut--;
            push();
            lines.push(rest.slice(0, cut));
            rest = rest.slice(cut);
          }
          const next = line ? line + " " + rest : rest;
          if (line && measure(next) > maxWidth) {
            push();
            line = rest;
          } else {
            line = next;
          }
        });
        push();
      });
    return lines;
  };

  const emptyCell = () => ({ bmp: null, zoom: 1, ox: 0, oy: 0 });

  const drawText = (ctx, tx, w, h) => {
    const px = Math.max(12, Math.round(tx.size * w));
    ctx.font = "bold " + px + "px " + FONT;
    const content = tx.upper ? tx.text.toUpperCase() : tx.text;
    const lines = wrapLines((s) => ctx.measureText(s).width, content, w * 0.94);
    const lh = px * 1.12;
    const total = lh * lines.length;
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.lineJoin = "round";
    ctx.miterLimit = 2;
    let widest = 0;
    lines.forEach((line, i) => {
      const y = tx.y * h - total / 2 + lh * (i + 0.5);
      if (tx.outline) {
        ctx.lineWidth = Math.max(2, px / 7);
        ctx.strokeStyle = tx.color.toLowerCase() === "#000000" ? "#ffffff" : "#000000";
        ctx.strokeText(line, tx.x * w, y);
      }
      ctx.fillStyle = tx.color;
      ctx.fillText(line, tx.x * w, y);
      widest = Math.max(widest, ctx.measureText(line).width);
    });
    tx.box = { x: tx.x * w - widest / 2, y: tx.y * h - total / 2, w: widest, h: total };
  };

  const render = (state, ctx, w, h, preview) => {
    ctx.fillStyle = state.bg;
    ctx.fillRect(0, 0, w, h);
    const rects = cellRects(state.layout, w, h, state.gap);
    rects.forEach((cell, i) => {
      const c = state.cells[i];
      ctx.save();
      ctx.beginPath();
      ctx.rect(cell.x, cell.y, cell.w, cell.h);
      ctx.clip();
      if (c && c.bmp) {
        const r = coverRect(c.bmp.width, c.bmp.height, cell, c.zoom, c.ox, c.oy);
        ctx.drawImage(c.bmp, r.x, r.y, r.w, r.h);
      } else if (preview) {
        ctx.fillStyle = "#2b2b2b";
        ctx.fillRect(cell.x, cell.y, cell.w, cell.h);
        ctx.fillStyle = "#8a8a8a";
        ctx.font = "bold " + Math.round(Math.min(cell.w, cell.h) / 4) + "px sans-serif";
        ctx.textAlign = "center";
        ctx.textBaseline = "middle";
        ctx.fillText("+", cell.x + cell.w / 2, cell.y + cell.h / 2);
      }
      ctx.restore();
      if (preview && state.sel.kind === "cell" && state.sel.index === i && rects.length > 1) {
        ctx.save();
        ctx.strokeStyle = "#5b8def";
        ctx.lineWidth = 6;
        ctx.strokeRect(cell.x + 3, cell.y + 3, cell.w - 6, cell.h - 6);
        ctx.restore();
      }
    });
    state.texts.forEach((tx) => {
      ctx.save();
      drawText(ctx, tx, w, h);
      ctx.restore();
    });
    if (preview && state.sel.kind === "text" && state.texts[state.sel.index] && state.texts[state.sel.index].box) {
      const b = state.texts[state.sel.index].box;
      ctx.save();
      ctx.strokeStyle = "#5b8def";
      ctx.lineWidth = 3;
      ctx.setLineDash([12, 8]);
      ctx.strokeRect(b.x - 8, b.y - 8, b.w + 16, b.h + 16);
      ctx.restore();
    }
  };

  const el = (tag, props, ...kids) => {
    const node = document.createElement(tag);
    Object.entries(props || {}).forEach(([k, v]) => {
      if (k === "class") node.className = v;
      else if (k === "text") node.textContent = v;
      else if (k in node) node[k] = v;
      else node.setAttribute(k, v);
    });
    kids.forEach((kid) => {
      if (kid) node.append(kid);
    });
    return node;
  };

  const labelled = (text, control) => el("label", { class: "ed-field" }, el("span", { text }), control);

  const thumb = (bmp) => {
    const c = document.createElement("canvas");
    c.width = 96;
    c.height = 64;
    const r = coverRect(bmp.width, bmp.height, { x: 0, y: 0, w: 96, h: 64 }, 1, 0, 0);
    c.getContext("2d").drawImage(bmp, r.x, r.y, r.w, r.h);
    return c;
  };

  const layoutThumb = (key) => {
    const c = document.createElement("canvas");
    c.width = 54;
    c.height = 36;
    const ctx = c.getContext("2d");
    ctx.fillStyle = "#8a8fa8";
    cellRects(key, c.width, c.height, 4).forEach((r) => ctx.fillRect(r.x, r.y, r.w, r.h));
    return c;
  };

  const fetchBitmap = async (url, t) => {
    let res;
    try {
      res = await fetch(url, { credentials: "same-origin" });
    } catch (err) {
      console.error("editor fetch", url, err);
      throw new Error(t("network_error"));
    }
    if (!res.ok) throw new Error(t("ed_failed"));
    return createImageBitmap(await res.blob());
  };

  const transformed = async (bmp, op) => {
    const swap = op === "left" || op === "right";
    const c = document.createElement("canvas");
    c.width = swap ? bmp.height : bmp.width;
    c.height = swap ? bmp.width : bmp.height;
    const ctx = c.getContext("2d");
    if (op === "right") {
      ctx.translate(c.width, 0);
      ctx.rotate(Math.PI / 2);
    } else if (op === "left") {
      ctx.translate(0, c.height);
      ctx.rotate(-Math.PI / 2);
    } else {
      ctx.translate(c.width, 0);
      ctx.scale(-1, 1);
    }
    ctx.drawImage(bmp, 0, 0);
    return createImageBitmap(c);
  };

  const open = (deps) => {
    const { t, api, upload, toast, icon, episodeId, hasStills, chooseStill, stillUrl, initialUrl, initialFile, onDone } = deps;
    const state = { layout: "single", aspect: "auto", gap: 0, bg: "#000000", cells: [emptyCell()], texts: [], sel: { kind: "cell", index: 0 } };
    const tray = [];
    const owned = [];
    const history = [];
    let lastKey = "";
    let lastAt = 0;
    let tool = "crop";

    const dlg = el("dialog", { class: "editor-dialog modal" });
    dlg.setAttribute("aria-label", t("ed_title"));
    const canvas = el("canvas", { class: "editor-canvas" });
    const ctx = canvas.getContext("2d");

    const size = () => {
      const first = state.cells[0] && state.cells[0].bmp;
      return outputSize(state.aspect, first ? first.width : 0, first ? first.height : 0, state.layout);
    };

    const redraw = () => {
      const s = size();
      if (canvas.width !== s.w) canvas.width = s.w;
      if (canvas.height !== s.h) canvas.height = s.h;
      render(state, ctx, s.w, s.h, true);
    };

    const aspectSelect = el("select", {});
    Object.keys(ASPECTS).forEach((key) => aspectSelect.append(el("option", { value: key, text: key === "auto" ? t("ed_a_auto") : key })));
    const zoomInput = el("input", { type: "range", min: 1, max: 6, step: 0.02, value: 1 });
    const bgInput = el("input", { type: "color", value: state.bg });
    const gapInput = el("input", { type: "range", min: 0, max: 48, step: 2, value: 0 });
    const textArea = el("textarea", { rows: 2, maxLength: 200 });
    const sizeInput = el("input", { type: "range", min: 0.03, max: 0.2, step: 0.005, value: 0.09 });
    const colorInput = el("input", { type: "color", value: "#ffffff" });
    const outlineBox = el("input", { type: "checkbox", checked: true });
    const upperBox = el("input", { type: "checkbox", checked: true });
    const addFiles = el("input", { type: "file", accept: "image/*", multiple: true, hidden: true });
    const trayBox = el("div", { class: "ed-tray" });
    const layoutBox = el("div", { class: "ed-layouts" });
    const textControls = el("div", { class: "ed-textcontrols", hidden: true });

    const selectedCell = () => (state.sel.kind === "cell" ? state.cells[state.sel.index] : null);
    const selectedText = () => (state.sel.kind === "text" ? state.texts[state.sel.index] : null);

    const sync = () => {
      const c = selectedCell();
      zoomInput.value = c ? c.zoom : 1;
      const tx = selectedText();
      textControls.hidden = !tx;
      if (tx) {
        textArea.value = tx.text;
        sizeInput.value = tx.size;
        colorInput.value = tx.color;
        outlineBox.checked = tx.outline;
        upperBox.checked = tx.upper;
      }
      layoutBox.querySelectorAll("button").forEach((b) => b.setAttribute("aria-pressed", b.dataset.layout === state.layout ? "true" : "false"));
    };

    const select = (kind, index) => {
      state.sel = { kind, index };
      sync();
      redraw();
    };

    const undoBtn = el("button", { type: "button", class: "icon-btn ed-undo", title: t("ed_undo"), disabled: true }, icon("fa-undo"));
    undoBtn.setAttribute("aria-label", t("ed_undo"));

    const snapshot = () => ({
      layout: state.layout,
      aspect: state.aspect,
      gap: state.gap,
      bg: state.bg,
      cells: state.cells.map((c) => ({ ...c })),
      texts: state.texts.map((x) => ({ ...x, box: null })),
      sel: { ...state.sel },
    });
    const checkpoint = (key) => {
      const now = Date.now();
      if (key && key === lastKey && now - lastAt < 800) {
        lastAt = now;
        return;
      }
      lastKey = key || "";
      lastAt = now;
      history.push(snapshot());
      if (history.length > 80) history.shift();
      undoBtn.disabled = false;
    };
    const undo = () => {
      const prev = history.pop();
      if (!prev) return;
      lastKey = "";
      Object.assign(state, prev);
      aspectSelect.value = state.aspect;
      bgInput.value = state.bg;
      gapInput.value = state.gap;
      undoBtn.disabled = history.length === 0;
      sync();
      redraw();
    };

    const rebuildTray = () => {
      trayBox.replaceChildren();
      tray.forEach((bmp) => {
        const b = el("button", { type: "button", class: "ed-thumb" });
        b.append(thumb(bmp));
        b.addEventListener("click", () => {
          checkpoint();
          let index = state.sel.kind === "cell" ? state.sel.index : -1;
          if (index < 0) index = state.cells.findIndex((c) => !c.bmp);
          if (index < 0) index = 0;
          state.cells[index] = { bmp, zoom: 1, ox: 0, oy: 0 };
          select("cell", index);
        });
        trayBox.append(b);
      });
    };

    const addBitmap = (bmp) => {
      checkpoint();
      tray.push(bmp);
      rebuildTray();
      let index = state.cells.findIndex((c) => !c.bmp);
      if (index < 0) index = state.sel.kind === "cell" ? state.sel.index : 0;
      state.cells[index] = { bmp, zoom: 1, ox: 0, oy: 0 };
      select("cell", index);
    };

    const setLayout = (layout) => {
      checkpoint();
      state.layout = layout;
      const n = LAYOUTS[layout].length;
      while (state.cells.length < n) state.cells.push(emptyCell());
      state.cells.length = n;
      if (state.sel.kind === "cell" && state.sel.index >= n) state.sel.index = 0;
      sync();
      redraw();
    };

    const newText = (text, y) => {
      checkpoint();
      state.texts.push({ text, x: 0.5, y, size: 0.09, color: "#ffffff", outline: true, upper: true, box: null });
      select("text", state.texts.length - 1);
    };

    Object.keys(LAYOUTS).forEach((key) => {
      const b = el("button", { type: "button", class: "ed-layout", title: t("ed_l_" + key) }, layoutThumb(key));
      b.dataset.layout = key;
      b.setAttribute("aria-label", t("ed_l_" + key));
      b.addEventListener("click", () => setLayout(key));
      layoutBox.append(b);
    });

    const on = (node, type, fn) => node.addEventListener(type, fn);
    on(aspectSelect, "change", () => {
      checkpoint();
      state.aspect = aspectSelect.value;
      redraw();
    });
    on(bgInput, "input", () => {
      checkpoint("bg");
      state.bg = bgInput.value;
      redraw();
    });
    on(gapInput, "input", () => {
      checkpoint("gap");
      state.gap = Number(gapInput.value);
      redraw();
    });
    on(zoomInput, "input", () => {
      const c = selectedCell();
      if (c) {
        checkpoint("zoom");
        c.zoom = Number(zoomInput.value);
        redraw();
      }
    });
    on(textArea, "input", () => {
      const tx = selectedText();
      if (tx) {
        checkpoint("text");
        tx.text = textArea.value;
        redraw();
      }
    });
    on(sizeInput, "input", () => {
      const tx = selectedText();
      if (tx) {
        checkpoint("size");
        tx.size = Number(sizeInput.value);
        redraw();
      }
    });
    on(colorInput, "input", () => {
      const tx = selectedText();
      if (tx) {
        checkpoint("color");
        tx.color = colorInput.value;
        redraw();
      }
    });
    on(outlineBox, "change", () => {
      const tx = selectedText();
      if (tx) {
        checkpoint();
        tx.outline = outlineBox.checked;
        redraw();
      }
    });
    on(upperBox, "change", () => {
      const tx = selectedText();
      if (tx) {
        checkpoint();
        tx.upper = upperBox.checked;
        redraw();
      }
    });
    on(addFiles, "change", async () => {
      const files = Array.from(addFiles.files);
      addFiles.value = "";
      for (const file of files) {
        try {
          addBitmap(await createImageBitmap(file));
        } catch (err) {
          console.error("editor decode", err);
          toast(t("ed_failed"), "error");
        }
      }
    });

    const pointAt = (e) => {
      const r = canvas.getBoundingClientRect();
      return { x: ((e.clientX - r.left) * canvas.width) / r.width, y: ((e.clientY - r.top) * canvas.height) / r.height };
    };

    let drag = null;
    on(canvas, "pointerdown", (e) => {
      const p = pointAt(e);
      for (let i = state.texts.length - 1; i >= 0; i--) {
        const b = state.texts[i].box;
        if (b && p.x >= b.x - 12 && p.x <= b.x + b.w + 12 && p.y >= b.y - 12 && p.y <= b.y + b.h + 12) {
          checkpoint();
          setTool("text");
          select("text", i);
          drag = { kind: "text", index: i, dx: p.x - state.texts[i].x * canvas.width, dy: p.y - state.texts[i].y * canvas.height };
          canvas.setPointerCapture(e.pointerId);
          return;
        }
      }
      const rects = cellRects(state.layout, canvas.width, canvas.height, state.gap);
      const hit = rects.findIndex((r) => p.x >= r.x && p.x <= r.x + r.w && p.y >= r.y && p.y <= r.y + r.h);
      if (hit >= 0) {
        checkpoint();
        select("cell", hit);
        drag = { kind: "cell", index: hit, x: p.x, y: p.y };
        canvas.setPointerCapture(e.pointerId);
      }
    });
    on(canvas, "pointermove", (e) => {
      if (!drag) return;
      const p = pointAt(e);
      if (drag.kind === "text") {
        const tx = state.texts[drag.index];
        tx.x = clamp((p.x - drag.dx) / canvas.width, 0, 1);
        tx.y = clamp((p.y - drag.dy) / canvas.height, 0, 1);
      } else {
        const c = state.cells[drag.index];
        if (c && c.bmp) {
          const cell = cellRects(state.layout, canvas.width, canvas.height, state.gap)[drag.index];
          const r = coverRect(c.bmp.width, c.bmp.height, cell, c.zoom, c.ox, c.oy);
          if (r.slackX > 0) c.ox = clamp(c.ox + (p.x - drag.x) / r.slackX, -1, 1);
          if (r.slackY > 0) c.oy = clamp(c.oy + (p.y - drag.y) / r.slackY, -1, 1);
        }
        drag.x = p.x;
        drag.y = p.y;
      }
      redraw();
    });
    const endDrag = () => {
      drag = null;
    };
    on(canvas, "pointerup", endDrag);
    on(canvas, "pointercancel", endDrag);
    canvas.addEventListener(
      "wheel",
      (e) => {
        e.preventDefault();
        const factor = e.deltaY < 0 ? 1.06 : 0.94;
        const tx = selectedText();
        const c = selectedCell();
        if (tx || c) checkpoint("wheel");
        if (tx) tx.size = clamp(tx.size * factor, 0.03, 0.2);
        else if (c) c.zoom = clamp(c.zoom * factor, 1, 6);
        sync();
        redraw();
      },
      { passive: false }
    );

    const button = (label, className, onClick, iconName) => {
      const b = el("button", { type: "button", class: className });
      if (iconName) b.append(icon(iconName));
      b.append(document.createTextNode(label));
      b.addEventListener("click", () => onClick(b));
      return b;
    };

    const iconButton = (iconName, label, className, onClick) => {
      const b = el("button", { type: "button", class: className, title: label });
      b.setAttribute("aria-label", label);
      b.append(icon(iconName));
      b.addEventListener("click", () => onClick(b));
      return b;
    };

    const adjust = async (op) => {
      const c = selectedCell();
      if (!c || !c.bmp) return;
      try {
        const next = await transformed(c.bmp, op);
        checkpoint();
        owned.push(next);
        state.cells[state.sel.index] = { bmp: next, zoom: 1, ox: 0, oy: 0 };
        sync();
        redraw();
      } catch (err) {
        console.error("editor transform", err);
        toast(t("ed_failed"), "error");
      }
    };

    const addFromDevice = () => addFiles.click();
    const addMoment = () =>
      chooseStill(async (file) => {
        try {
          addBitmap(await fetchBitmap(stillUrl(file), t));
        } catch (err) {
          toast(err.message, "error");
        }
      });

    const cropPanel = el(
      "div",
      { class: "ed-panel" },
      labelled(t("ed_aspect"), aspectSelect),
      labelled(t("ed_zoom"), zoomInput),
      el(
        "div",
        { class: "ed-row" },
        button(t("ed_rotate_left"), "btn small ghost", () => adjust("left"), "fa-rotate-left"),
        button(t("ed_rotate_right"), "btn small ghost", () => adjust("right"), "fa-rotate-right"),
        button(t("ed_flip"), "btn small ghost", () => adjust("flip"), "fa-flip")
      ),
      button(
        t("ed_reset"),
        "btn small ghost",
        () => {
          const c = selectedCell();
          if (c) {
            checkpoint();
            c.zoom = 1;
            c.ox = 0;
            c.oy = 0;
            sync();
            redraw();
          }
        }
      )
    );
    const collagePanel = el(
      "div",
      { class: "ed-panel" },
      el("span", { class: "ed-label", text: t("ed_layout") }),
      layoutBox,
      el("div", { class: "ed-row" }, button(t("ed_add_device"), "btn small ghost", addFromDevice, "fa-upload"), hasStills ? button(t("ed_add_moment"), "btn small ghost", addMoment, "fa-film") : null),
      trayBox,
      labelled(t("ed_background"), bgInput),
      labelled(t("ed_gap"), gapInput),
      button(t("ed_clear"), "btn small ghost", () => {
        if (selectedCell()) {
          checkpoint();
          state.cells[state.sel.index] = emptyCell();
          sync();
          redraw();
        }
      }),
      addFiles
    );
    const textPanel = el(
      "div",
      { class: "ed-panel" },
      el(
        "div",
        { class: "ed-row" },
        button(t("ed_top"), "btn small ghost", () => newText(t("ed_top_default"), 0.12)),
        button(t("ed_bottom"), "btn small ghost", () => newText(t("ed_bottom_default"), 0.88)),
        button(t("ed_add_text"), "btn small ghost", () => newText(t("ed_text_default"), 0.5))
      ),
      textControls
    );
    textControls.append(
      labelled(t("ed_text_content"), textArea),
      labelled(t("ed_size"), sizeInput),
      el(
        "div",
        { class: "ed-inline" },
        labelled(t("ed_color"), colorInput),
        el("label", { class: "ed-check" }, outlineBox, el("span", { text: t("ed_outline") })),
        el("label", { class: "ed-check" }, upperBox, el("span", { text: t("ed_upper") }))
      ),
      iconButton("fa-trash", t("ed_delete_text"), "icon-btn ed-delete", () => {
        if (state.sel.kind !== "text") return;
        checkpoint();
        state.texts.splice(state.sel.index, 1);
        select("cell", 0);
      })
    );

    const panels = { crop: cropPanel, collage: collagePanel, text: textPanel };
    const toolButtons = {};
    function setTool(next) {
      tool = next;
      Object.entries(panels).forEach(([k, node]) => {
        node.hidden = k !== tool;
        toolButtons[k].setAttribute("aria-pressed", k === tool ? "true" : "false");
      });
    }
    const toolbar = el("div", { class: "ed-tools", role: "toolbar" });
    [
      ["crop", "fa-crop", t("ed_tool_crop")],
      ["collage", "fa-collage", t("ed_tool_collage")],
      ["text", "fa-text", t("ed_tool_text")],
    ].forEach(([key, iconName, label]) => {
      toolButtons[key] = iconButton(iconName, label, "ed-tool", () => setTool(key));
      toolbar.append(toolButtons[key]);
    });

    const cancel = el("button", { type: "button", class: "btn ghost", text: t("ed_cancel") });
    const use = el("button", { type: "button", class: "btn", text: t("ed_use") });
    dlg.append(
      el("div", { class: "ed-head" }, toolbar, undoBtn),
      el("div", { class: "editor-body" }, el("div", { class: "editor-stage" }, canvas), el("div", { class: "editor-panel" }, cropPanel, collagePanel, textPanel)),
      el("p", { class: "muted", text: t("ed_drag_hint") }),
      el("div", { class: "modal-actions" }, cancel, use)
    );

    const close = () => {
      tray.forEach((b) => b.close());
      owned.forEach((b) => b.close());
      dlg.remove();
    };
    on(undoBtn, "click", undo);
    on(dlg, "cancel", close);
    on(cancel, "click", () => {
      dlg.close();
      close();
    });

    on(use, "click", async () => {
      if (!state.cells.some((c) => c.bmp) && state.texts.length === 0) {
        toast(t("ed_empty"), "error");
        return;
      }
      use.disabled = true;
      try {
        const s = size();
        const out = document.createElement("canvas");
        out.width = s.w;
        out.height = s.h;
        render(state, out.getContext("2d"), s.w, s.h, false);
        const blob = await new Promise((resolve) => out.toBlob(resolve, "image/jpeg", 0.92));
        if (!blob) throw new Error(t("ed_failed"));
        const data = await upload("/api/pics", new File([blob], "picture.jpg", { type: "image/jpeg" }));
        dlg.close();
        close();
        onDone(data);
      } catch (err) {
        toast(err.message, "error");
        use.disabled = false;
      }
    });

    document.body.append(dlg);
    dlg.showModal();
    setTool("crop");
    sync();
    redraw();
    if (initialFile) {
      createImageBitmap(initialFile)
        .then((bmp) => addBitmap(bmp))
        .catch((err) => {
          console.error("editor decode", err);
          toast(t("ed_failed"), "error");
        });
    } else if (initialUrl) {
      fetchBitmap(initialUrl, t)
        .then((bmp) => addBitmap(bmp))
        .catch((err) => toast(err.message, "error"));
    }
  };

  window.TVTEditor = { open, pure: { LAYOUTS, ASPECTS, outputSize, cellRects, coverRect, wrapLines, clamp } };
})();
