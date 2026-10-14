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
    const ctx = c.getContext("2d");
    const r = coverRect(bmp.width, bmp.height, { x: 0, y: 0, w: 96, h: 64 }, 1, 0, 0);
    ctx.drawImage(bmp, r.x, r.y, r.w, r.h);
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

  const open = (deps) => {
    const { t, api, upload, toast, episodeId, initial, onDone } = deps;
    const state = { layout: "single", aspect: "auto", gap: 0, bg: "#000000", cells: [emptyCell()], texts: [], sel: { kind: "cell", index: 0 } };
    const tray = [];

    const dlg = el("dialog", { class: "editor-dialog modal" });
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

    const layoutSelect = el("select", {});
    Object.keys(LAYOUTS).forEach((key) => layoutSelect.append(el("option", { value: key, text: t("ed_l_" + key) })));
    const aspectSelect = el("select", {});
    Object.keys(ASPECTS).forEach((key) => aspectSelect.append(el("option", { value: key, text: key === "auto" ? t("ed_a_auto") : key })));
    const bgInput = el("input", { type: "color", value: state.bg });
    const gapInput = el("input", { type: "range", min: 0, max: 48, step: 2, value: 0 });

    const addFiles = el("input", { type: "file", accept: "image/*", multiple: true, hidden: true });
    const trayBox = el("div", { class: "ed-tray" });
    const zoomInput = el("input", { type: "range", min: 1, max: 6, step: 0.02, value: 1 });
    const cellBox = el("fieldset", { class: "ed-group" }, el("legend", { text: t("ed_picture") }));
    const textBox = el("fieldset", { class: "ed-group" }, el("legend", { text: t("ed_text") }));
    const textArea = el("textarea", { rows: 2, maxLength: 200 });
    const sizeInput = el("input", { type: "range", min: 0.03, max: 0.2, step: 0.005, value: 0.09 });
    const colorInput = el("input", { type: "color", value: "#ffffff" });
    const outlineBox = el("input", { type: "checkbox", checked: true });
    const upperBox = el("input", { type: "checkbox", checked: true });
    const textControls = el("div", { class: "ed-textcontrols", hidden: true });

    const selectedCell = () => (state.sel.kind === "cell" ? state.cells[state.sel.index] : null);
    const selectedText = () => (state.sel.kind === "text" ? state.texts[state.sel.index] : null);

    const sync = () => {
      const c = selectedCell();
      cellBox.hidden = !c;
      if (c) zoomInput.value = c.zoom;
      const tx = selectedText();
      textControls.hidden = !tx;
      if (tx) {
        textArea.value = tx.text;
        sizeInput.value = tx.size;
        colorInput.value = tx.color;
        outlineBox.checked = tx.outline;
        upperBox.checked = tx.upper;
      }
    };

    const select = (kind, index) => {
      state.sel = { kind, index };
      sync();
      redraw();
    };

    const rebuildTray = () => {
      trayBox.replaceChildren();
      tray.forEach((bmp) => {
        const b = el("button", { type: "button", class: "ed-thumb" });
        b.append(thumb(bmp));
        b.addEventListener("click", () => {
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
      tray.push(bmp);
      rebuildTray();
      let index = state.cells.findIndex((c) => !c.bmp);
      if (index < 0) index = state.sel.kind === "cell" ? state.sel.index : 0;
      state.cells[index] = { bmp, zoom: 1, ox: 0, oy: 0 };
      select("cell", index);
    };

    const setLayout = (layout) => {
      state.layout = layout;
      const n = LAYOUTS[layout].length;
      while (state.cells.length < n) state.cells.push(emptyCell());
      state.cells.length = n;
      if (state.sel.kind === "cell" && state.sel.index >= n) state.sel.index = 0;
      sync();
      redraw();
    };

    const newText = (text, y) => {
      state.texts.push({ text, x: 0.5, y, size: 0.09, color: "#ffffff", outline: true, upper: true, box: null });
      select("text", state.texts.length - 1);
    };

    layoutSelect.addEventListener("change", () => setLayout(layoutSelect.value));
    aspectSelect.addEventListener("change", () => {
      state.aspect = aspectSelect.value;
      redraw();
    });
    bgInput.addEventListener("input", () => {
      state.bg = bgInput.value;
      redraw();
    });
    gapInput.addEventListener("input", () => {
      state.gap = Number(gapInput.value);
      redraw();
    });
    zoomInput.addEventListener("input", () => {
      const c = selectedCell();
      if (c) {
        c.zoom = Number(zoomInput.value);
        redraw();
      }
    });
    textArea.addEventListener("input", () => {
      const tx = selectedText();
      if (tx) {
        tx.text = textArea.value;
        redraw();
      }
    });
    sizeInput.addEventListener("input", () => {
      const tx = selectedText();
      if (tx) {
        tx.size = Number(sizeInput.value);
        redraw();
      }
    });
    colorInput.addEventListener("input", () => {
      const tx = selectedText();
      if (tx) {
        tx.color = colorInput.value;
        redraw();
      }
    });
    outlineBox.addEventListener("change", () => {
      const tx = selectedText();
      if (tx) {
        tx.outline = outlineBox.checked;
        redraw();
      }
    });
    upperBox.addEventListener("change", () => {
      const tx = selectedText();
      if (tx) {
        tx.upper = upperBox.checked;
        redraw();
      }
    });

    addFiles.addEventListener("change", async () => {
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
    canvas.addEventListener("pointerdown", (e) => {
      const p = pointAt(e);
      for (let i = state.texts.length - 1; i >= 0; i--) {
        const b = state.texts[i].box;
        if (b && p.x >= b.x - 12 && p.x <= b.x + b.w + 12 && p.y >= b.y - 12 && p.y <= b.y + b.h + 12) {
          select("text", i);
          drag = { kind: "text", index: i, dx: p.x - state.texts[i].x * canvas.width, dy: p.y - state.texts[i].y * canvas.height };
          canvas.setPointerCapture(e.pointerId);
          return;
        }
      }
      const rects = cellRects(state.layout, canvas.width, canvas.height, state.gap);
      const hit = rects.findIndex((r) => p.x >= r.x && p.x <= r.x + r.w && p.y >= r.y && p.y <= r.y + r.h);
      if (hit >= 0) {
        select("cell", hit);
        drag = { kind: "cell", index: hit, x: p.x, y: p.y };
        canvas.setPointerCapture(e.pointerId);
      }
    });
    canvas.addEventListener("pointermove", (e) => {
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
    canvas.addEventListener("pointerup", endDrag);
    canvas.addEventListener("pointercancel", endDrag);
    canvas.addEventListener(
      "wheel",
      (e) => {
        e.preventDefault();
        const factor = e.deltaY < 0 ? 1.06 : 0.94;
        const tx = selectedText();
        const c = selectedCell();
        if (tx) {
          tx.size = clamp(tx.size * factor, 0.03, 0.2);
        } else if (c) {
          c.zoom = clamp(c.zoom * factor, 1, 6);
        }
        sync();
        redraw();
      },
      { passive: false }
    );

    const chooseStill = async () => {
      let data;
      try {
        data = await api("/api/episode/" + episodeId + "/stills", null, "GET");
      } catch (err) {
        toast(err.message, "error");
        return;
      }
      const picker = el("dialog", { class: "stills-dialog modal" }, el("h2", { text: t("pic_stills_title") }));
      if (data.stills.length === 0) {
        picker.append(el("p", { class: "muted", text: t("pic_stills_none") }));
      } else {
        const grid = el("div", { class: "stills-grid" });
        data.stills.forEach((file) => {
          const im = el("img", { loading: "lazy", alt: "", src: "/img/w300" + file });
          const choice = el("button", { type: "button", class: "still-choice" }, im);
          choice.addEventListener("click", async () => {
            choice.disabled = true;
            try {
              addBitmap(await fetchBitmap("/api/episode/" + episodeId + "/still?file=" + encodeURIComponent(file), t));
              picker.close();
            } catch (err) {
              toast(err.message, "error");
            }
            choice.disabled = false;
          });
          grid.append(choice);
        });
        picker.append(grid);
      }
      const close = el("button", { type: "button", class: "btn ghost", text: t("close") });
      close.addEventListener("click", () => picker.close());
      picker.append(el("div", { class: "modal-actions" }, close));
      picker.addEventListener("close", () => picker.remove());
      document.body.append(picker);
      picker.showModal();
    };

    const stillsBtn = el("button", { type: "button", class: "btn small ghost", text: t("pic_stills") });
    stillsBtn.addEventListener("click", chooseStill);
    stillsBtn.hidden = !episodeId;
    const addBtn = el("button", { type: "button", class: "btn small ghost", text: t("ed_add_images") });
    addBtn.addEventListener("click", () => addFiles.click());
    const clearBtn = el("button", { type: "button", class: "btn small ghost", text: t("ed_clear") });
    clearBtn.addEventListener("click", () => {
      const c = selectedCell();
      if (c) {
        state.cells[state.sel.index] = emptyCell();
        sync();
        redraw();
      }
    });
    const resetBtn = el("button", { type: "button", class: "btn small ghost", text: t("ed_reset") });
    resetBtn.addEventListener("click", () => {
      const c = selectedCell();
      if (c) {
        c.zoom = 1;
        c.ox = 0;
        c.oy = 0;
        sync();
        redraw();
      }
    });
    const topBtn = el("button", { type: "button", class: "btn small ghost", text: t("ed_top") });
    topBtn.addEventListener("click", () => newText(t("ed_top_default"), 0.12));
    const bottomBtn = el("button", { type: "button", class: "btn small ghost", text: t("ed_bottom") });
    bottomBtn.addEventListener("click", () => newText(t("ed_bottom_default"), 0.88));
    const freeBtn = el("button", { type: "button", class: "btn small ghost", text: t("ed_add_text") });
    freeBtn.addEventListener("click", () => newText(t("ed_text_default"), 0.5));
    const deleteBtn = el("button", { type: "button", class: "btn small danger", text: t("ed_delete_text") });
    deleteBtn.addEventListener("click", () => {
      if (state.sel.kind !== "text") return;
      state.texts.splice(state.sel.index, 1);
      select("cell", 0);
    });

    cellBox.append(labelled(t("ed_zoom"), zoomInput), el("div", { class: "ed-row" }, resetBtn, clearBtn));
    textControls.append(
      labelled(t("ed_text_content"), textArea),
      labelled(t("ed_size"), sizeInput),
      labelled(t("ed_color"), colorInput),
      el("label", { class: "ed-check" }, outlineBox, el("span", { text: t("ed_outline") })),
      el("label", { class: "ed-check" }, upperBox, el("span", { text: t("ed_upper") })),
      deleteBtn
    );
    textBox.append(el("div", { class: "ed-row" }, topBtn, bottomBtn, freeBtn), textControls);

    const panel = el(
      "div",
      { class: "editor-panel" },
      el(
        "fieldset",
        { class: "ed-group" },
        el("legend", { text: t("ed_layout") }),
        labelled(t("ed_layout"), layoutSelect),
        labelled(t("ed_aspect"), aspectSelect),
        labelled(t("ed_background"), bgInput),
        labelled(t("ed_gap"), gapInput)
      ),
      el("fieldset", { class: "ed-group" }, el("legend", { text: t("ed_images") }), el("div", { class: "ed-row" }, addBtn, stillsBtn), trayBox, el("p", { class: "muted", text: t("ed_gif_note") }), addFiles),
      cellBox,
      textBox
    );

    const cancel = el("button", { type: "button", class: "btn ghost", text: t("ed_cancel") });
    const use = el("button", { type: "button", class: "btn", text: t("ed_use") });
    dlg.append(
      el("h2", { text: t("ed_title") }),
      el("p", { class: "muted", text: t("ed_drag_hint") }),
      el("div", { class: "editor-body" }, el("div", { class: "editor-stage" }, canvas), panel),
      el("div", { class: "modal-actions" }, cancel, use)
    );

    const close = () => {
      tray.forEach((b) => b.close());
      state.cells.forEach((c) => {
        if (c.bmp && !tray.includes(c.bmp)) c.bmp.close();
      });
      dlg.remove();
    };
    dlg.addEventListener("cancel", close);
    cancel.addEventListener("click", () => {
      dlg.close();
      close();
    });

    use.addEventListener("click", async () => {
      const hasPicture = state.cells.some((c) => c.bmp);
      if (!hasPicture && state.texts.length === 0) {
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
        const data = await upload("/api/pics", new File([blob], "collage.jpg", { type: "image/jpeg" }));
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
    sync();
    redraw();
    if (initial) {
      fetchBitmap(initial, t)
        .then((bmp) => addBitmap(bmp))
        .catch((err) => toast(err.message, "error"));
    }
  };

  window.TVTEditor = { open, pure: { LAYOUTS, ASPECTS, outputSize, cellRects, coverRect, wrapLines, clamp } };
})();
