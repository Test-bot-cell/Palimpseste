// Palimpseste — overlay d'édition in-place (vanilla TypeScript, zéro framework).
//
// Chargé uniquement par le serveur d'édition, jamais présent en production. Il
// transforme chaque région [data-slot] de la vraie page en éditeur, renvoie le
// HTML édité à l'API fragments et reflète la forme canonique que le serveur
// renvoie — l'auteur voit toujours exactement ce qui est stocké (§9).
//
// Isolation (§9 « Shadow DOM intégral ») : tout le chrome vit dans un shadow
// root ; les affordances d'édition sur la page sont des styles inline posés sur
// des nœuds appartenant au template (jamais au fragment sauvegardé). Aucune
// feuille de style n'entre dans le DOM clair : le CSS du thème et celui de
// l'éditeur ne partagent aucune cascade, dans aucun sens.
//
// Ergonomie cognitive — les principes appliqués, pour le maintainer :
//   - divulgation progressive : au repos, une barre discrète (nom, page, état) ;
//     les commandes de mise en forme n'apparaissent qu'au focus d'une région
//     richtext — jamais d'options qui ne s'appliquent pas au contexte ;
//   - reconnaissance plutôt que rappel : libellés en toutes lettres et
//     raccourcis affichés, pas d'iconographie à décoder ;
//   - visibilité de l'état : un unique point d'état, toujours au même endroit,
//     en mots (« prêt », « modifié », « enregistré », « page régénérée ») avec
//     un code couleur constant (ambre = non sauvegardé, vert = ok, rouge =
//     erreur) ;
//   - prévention des pertes : garde beforeunload, confirmation avant de changer
//     de page avec des modifications en attente, validation du schéma d'URL
//     avant l'insertion d'un lien (l'UI ne propose jamais ce que le serveur
//     refuserait, §9).

type PageEntry = { id: string; route: string; title: string };
type SlotDecl = { type: string; blocks?: string[] };
type ParamSchema = { kind: string; min?: number; max?: number; values?: string[] };
type BlockSchema = { computed: boolean; elements: string[]; params: Record<string, ParamSchema> };
type PageMeta = { title: string; description: string; ogImage?: string };
type OverlayConfig = {
  page: string;
  csrf: string;
  pages: PageEntry[];
  slots: Record<string, SlotDecl>;
  blocks: Record<string, BlockSchema>;
  meta: PageMeta;
  publish: boolean;
  ai: boolean;
};

const cfgEl = document.getElementById("_pal-config");
if (!cfgEl) {
  throw new Error("palimpseste: overlay config missing");
}
const CFG: OverlayConfig = JSON.parse(cfgEl.textContent || "{}");
const API = "/api";

function fragmentURL(slot: string): string {
  return `${API}/fragments/${encodeURIComponent(CFG.page)}/${encodeURIComponent(slot)}`;
}

const state = {
  dirty: new Set<string>(),
  saving: false,
  ignoreReloadUntil: 0, // suppress our own save-triggered reload
  activeSlot: "", // last region the author touched — the history panel's target
};

// ---- affordances d'édition (styles inline sur nœuds du template) -------------

const IDLE_OUTLINE = "1px dashed rgba(53,132,228,.55)";
const FOCUS_OUTLINE = "2px solid rgba(53,132,228,.9)";
const DIRTY_OUTLINE = "2px solid rgba(246,211,45,.9)";

function paintRegion(region: HTMLElement): void {
  const slot = region.getAttribute("data-slot") || "";
  const focused = region === document.activeElement;
  const dirty = state.dirty.has(slot);
  region.style.outline = dirty ? DIRTY_OUTLINE : focused ? FOCUS_OUTLINE : IDLE_OUTLINE;
  region.style.outlineOffset = "2px";
  region.style.minHeight = "1em"; // an empty slot stays visible, hence editable
}

// ---- chrome (Shadow DOM) ------------------------------------------------------

const host = document.createElement("div");
host.id = "_pal-host";
host.style.cssText = "position:fixed;left:0;right:0;bottom:0;z-index:2147483647;";
document.body.appendChild(host);

const shadow = host.attachShadow({ mode: "open" });
shadow.innerHTML = `
  <style>
    :host { all: initial; }
    * { box-sizing: border-box; }
    .bar {
      font: 13px/1.4 system-ui, -apple-system, sans-serif;
      display: flex; gap: .5rem; align-items: center;
      background: #1e1e1e; color: #e6e6e6;
      padding: .5rem .75rem; box-shadow: 0 -2px 12px rgba(0,0,0,.35);
    }
    strong { color: #fff; letter-spacing: .01em; }
    .grow { flex: 1; }
    button, select, input {
      font: inherit; color: #e6e6e6; background: #333;
      border: 1px solid #555; border-radius: 6px; padding: .28rem .6rem;
    }
    button, select { cursor: pointer; }
    button:hover { background: #3584e4; border-color: #3584e4; }
    button:disabled { opacity: .45; cursor: default; }
    button:disabled:hover { background: #333; border-color: #555; }
    button[aria-pressed="true"] { background: #3584e4; border-color: #3584e4; }
    .fmt { display: flex; gap: .25rem; }
    .fmt[hidden] { display: none; }
    .status { font-variant-numeric: tabular-nums; opacity: .85; min-width: 12ch; }
    .status.dirty { color: #f6d32d; opacity: 1; }
    .status.saved { color: #8ff0a4; opacity: 1; }
    .status.error { color: #ff7b6b; opacity: 1; }
    /* Panneaux : divulgation progressive — un seul ouvert à la fois, au-dessus
       de la barre, jamais mêlé au contenu de la page. */
    .panel {
      font: 13px/1.5 system-ui, -apple-system, sans-serif;
      background: #262626; color: #e6e6e6;
      border-top: 1px solid #444; padding: .9rem 1rem;
      max-height: 45vh; overflow: auto;
      box-shadow: 0 -2px 12px rgba(0,0,0,.3);
    }
    .panel[hidden] { display: none; }
    .panel h2 { font-size: 13px; margin: 0 0 .6rem; color: #fff; font-weight: 600; }
    .row { display: grid; grid-template-columns: 14ch 1fr auto; gap: .5rem; align-items: center; margin: .3rem 0; }
    .row label { opacity: .85; }
    .row .val { font-variant-numeric: tabular-nums; opacity: .7; min-width: 4ch; text-align: right; }
    .swatch { width: 1.5rem; height: 1.5rem; border-radius: 4px; border: 1px solid #555; }
    .hint { opacity: .6; font-size: 12px; margin: .3rem 0 .6rem; }
    .find { margin: .25rem 0; padding: .35rem .5rem; border-radius: 5px; background: #2f2f2f; }
    .find.error { border-left: 3px solid #ff7b6b; }
    .find.warning, .find.warn { border-left: 3px solid #f6d32d; }
    .serp { background: #fff; color: #202124; border-radius: 6px; padding: .6rem .8rem; margin: .5rem 0; max-width: 42rem; }
    .serp .t { color: #1a0dab; font-size: 16px; line-height: 1.3; }
    .serp .u { color: #006621; font-size: 12px; }
    .serp .d { color: #4d5156; font-size: 13px; }
    textarea, input.full { width: 100%; }
    textarea { min-height: 3.5em; resize: vertical; }
    .stack-item { display: flex; gap: .4rem; align-items: center; padding: .3rem .4rem; margin: .25rem 0; background: #2f2f2f; border-radius: 5px; }
    .stack-item .name { flex: 1; }
    .cfg { display: grid; grid-template-columns: 12ch 1fr; gap: .4rem; align-items: center; margin: .2rem 0; }
  </style>
  <div class="panel" id="panel" hidden></div>
  <div class="bar" role="toolbar" aria-label="Palimpseste">
    <strong>Palimpseste</strong>
    <select id="pages" title="Aller à une page"></select>
    <span class="grow"></span>
    <span class="fmt" id="fmt" hidden>
      <button data-cmd="bold" title="Gras (Ctrl+B)">Gras</button>
      <button data-cmd="italic" title="Italique (Ctrl+I)">Italique</button>
      <button data-cmd="createLink" title="Insérer un lien">Lien</button>
      <button data-cmd="removeFormat" title="Enlever la mise en forme">Nettoyer</button>
    </span>
    <button id="btn-theme" title="Thème et tokens">Thème</button>
    <button id="btn-seo" title="Référencement de la page">SEO</button>
    <button id="btn-history" title="Historique de la région (§13)">Historique</button>
    <button id="btn-check" title="Vérifier la qualité (§11)">Vérifier</button>
    <button id="btn-publish" title="Publier vers le dépôt distant" hidden>Publier</button>
    <span class="status" id="status">prêt</span>
    <button id="save" disabled title="Enregistrer (Ctrl+S)">Enregistrer</button>
  </div>
`;

const byId = (id: string) => shadow.getElementById(id)!;
const statusEl = byId("status");
const saveBtn = byId("save") as HTMLButtonElement;
const fmtEl = byId("fmt");
const pagesEl = byId("pages") as HTMLSelectElement;
const panelEl = byId("panel");

for (const p of CFG.pages || []) {
  const opt = document.createElement("option");
  opt.value = p.route;
  opt.textContent = p.title || p.route;
  if (p.id === CFG.page) opt.selected = true;
  pagesEl.appendChild(opt);
}
pagesEl.addEventListener("change", () => {
  if (confirmDiscard()) {
    location.pathname = pagesEl.value;
  } else {
    // Snap the selector back: the visible state must never lie about the page.
    for (const o of pagesEl.options) o.selected = o.value === currentRoute();
  }
});
function currentRoute(): string {
  const p = (CFG.pages || []).find((x) => x.id === CFG.page);
  return p ? p.route : location.pathname;
}

// L'UI ne propose jamais ce que le serveur refuserait (§9) : gras/italique
// passent par execCommand — le contrat normalise <b>/<i> en <strong>/<em> côté
// serveur, la sémantique de l'auteur est préservée à l'octet près au retour —
// et le lien est validé au même schéma-whitelist que la sanitisation.
document.execCommand("styleWithCSS", false, "false");
document.execCommand("defaultParagraphSeparator", false, "p");

function linkURLAllowed(url: string): boolean {
  const u = url.trim();
  if (u === "" || u.startsWith("//")) return false;
  const m = /^([a-zA-Z][a-zA-Z0-9+.-]*):/.exec(u);
  if (!m) return true; // relative
  return ["http", "https", "mailto", "tel"].includes(m[1].toLowerCase());
}

for (const b of fmtEl.querySelectorAll("button")) {
  b.addEventListener("mousedown", (e) => e.preventDefault()); // preserve selection
  b.addEventListener("click", () => {
    const cmd = (b as HTMLElement).dataset.cmd!;
    if (cmd === "createLink") {
      const url = prompt("URL du lien (https, mailto, tel ou relative) :");
      if (!url) return;
      if (!linkURLAllowed(url)) {
        setStatus("URL refusée par le contrat", "error");
        return;
      }
      document.execCommand(cmd, false, url);
    } else {
      document.execCommand(cmd, false);
    }
  });
}

function setStatus(text: string, cls?: string): void {
  statusEl.textContent = text;
  statusEl.className = "status" + (cls ? " " + cls : "");
}
function confirmDiscard(): boolean {
  return state.dirty.size === 0 || confirm("Des modifications ne sont pas enregistrées. Continuer ?");
}

// ---- régions éditables ---------------------------------------------------------

function slotType(slot: string): string {
  return (CFG.slots || {})[slot]?.type || "richtext";
}

const regions = [...document.querySelectorAll<HTMLElement>("[data-slot]")];
for (const region of regions) {
  const slot = region.getAttribute("data-slot") || "";
  const type = slotType(slot);

  // Un slot stack est piloté par son propre éditeur (liste réordonnable de
  // blocs, §5.1) — pas de contenteditable de prose libre.
  if (type === "stack") {
    initStackRegion(region, slot);
    continue;
  }
  // Un slot image ouvre le sélecteur média (§9) ; un slot data ouvre la grille
  // type tableur — ni l'un ni l'autre n'est du contenteditable de prose.
  if (type === "image") {
    initImageRegion(region, slot);
    continue;
  }
  if (type === "data") {
    initDataRegion(region, slot);
    continue;
  }

  const plain = type === "plain";
  // Un slot plain est du texte nu, une ligne (§5.1) : plaintext-only où le
  // navigateur le supporte, et Entrée neutralisée partout.
  region.setAttribute("contenteditable", plain ? "plaintext-only" : "true");
  region.setAttribute("spellcheck", "false");
  paintRegion(region);

  region.addEventListener("focus", () => {
    fmtEl.hidden = plain; // la mise en forme n'existe pas pour du texte nu
    state.activeSlot = slot;
    paintRegion(region);
  });
  region.addEventListener("blur", () => paintRegion(region));
  if (plain) {
    region.addEventListener("keydown", (e) => {
      if (e.key === "Enter") e.preventDefault();
    });
  }
  region.addEventListener("input", () => {
    markDirty(slot);
    paintRegion(region);
  });
}

function markDirty(slot: string): void {
  state.dirty.add(slot);
  saveBtn.disabled = false;
  setStatus("modifié — Ctrl+S pour enregistrer", "dirty");
}

// ---- sauvegarde -----------------------------------------------------------------

async function saveRegion(region: HTMLElement): Promise<void> {
  const slot = region.getAttribute("data-slot") || "";
  const isStack = region.hasAttribute("data-pal-stack");
  const body = isStack ? stackFragment(region) : region.innerHTML;
  const res = await fetch(fragmentURL(slot), {
    method: "PUT",
    headers: { "Content-Type": "text/html; charset=utf-8", "X-Pal-CSRF": CFG.csrf },
    body, // raw edited HTML; the server sanitises and canonicalises
  });
  if (!res.ok) {
    throw new Error("HTTP " + res.status + ": " + (await res.text()).slice(0, 200));
  }
  const canonical = await res.text();
  if (isStack) {
    region.innerHTML = canonical; // reflect stored blocks, then rebuild the control UI
    renderStackItems(region, slot);
  } else {
    region.innerHTML = canonical; // reflect exactly what was stored
  }
  state.dirty.delete(slot);
  paintRegion(region);
}

async function saveAll(): Promise<void> {
  if (state.saving || state.dirty.size === 0) return;
  state.saving = true;
  saveBtn.disabled = true;
  setStatus("enregistrement…");
  state.ignoreReloadUntil = Date.now() + 2000;
  try {
    for (const region of regions) {
      if (state.dirty.has(region.getAttribute("data-slot") || "")) {
        await saveRegion(region);
      }
    }
    setStatus("enregistré", "saved");
  } catch (err) {
    console.error("palimpseste save failed:", err);
    setStatus("échec de l'enregistrement", "error");
    saveBtn.disabled = false;
  } finally {
    state.saving = false;
    state.ignoreReloadUntil = Date.now() + 2000;
  }
}

saveBtn.addEventListener("click", saveAll);
document.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
    e.preventDefault();
    saveAll();
  }
});
window.addEventListener("beforeunload", (e) => {
  if (state.dirty.size > 0) {
    e.preventDefault();
    e.returnValue = "";
  }
});

// ---- panneaux M2 (divulgation progressive : un seul ouvert à la fois) ----------

let openPanel: string | null = null;

function togglePanel(name: string, render: () => void): void {
  if (openPanel === name) {
    panelEl.hidden = true;
    openPanel = null;
    reflectPanelButtons();
    return;
  }
  openPanel = name;
  panelEl.hidden = false;
  reflectPanelButtons();
  render();
}
function reflectPanelButtons(): void {
  (byId("btn-theme") as HTMLButtonElement).setAttribute("aria-pressed", String(openPanel === "theme"));
  (byId("btn-seo") as HTMLButtonElement).setAttribute("aria-pressed", String(openPanel === "seo"));
  (byId("btn-check") as HTMLButtonElement).setAttribute("aria-pressed", String(openPanel === "check"));
}
function esc(s: string): string {
  return s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!);
}

byId("btn-theme").addEventListener("click", () => togglePanel("theme", renderThemePanel));
byId("btn-seo").addEventListener("click", () => togglePanel("seo", renderSEOPanel));
byId("btn-check").addEventListener("click", () => togglePanel("check", renderCheckPanel));
byId("btn-history").addEventListener("click", () => togglePanel("history", renderHistoryPanel));
const publishBtn = byId("btn-publish") as HTMLButtonElement;
if (CFG.publish) {
  publishBtn.hidden = false;
  publishBtn.addEventListener("click", publish);
}

// ---- panneau Thème : tokens en direct (§6) + bascule de thème (§5.3) ------------

async function renderThemePanel(): Promise<void> {
  panelEl.innerHTML = `<h2>Thème</h2><p class="hint">Chargement…</p>`;
  const [info, themes] = await Promise.all([
    fetch(`${API}/theme`).then((r) => r.json()),
    fetch(`${API}/themes`).then((r) => r.json()),
  ]);

  const tokenRows = Object.entries(info.tokens || {})
    .map(([name, t]: [string, any]) => {
      const control =
        t.type === "color"
          ? `<input type="color" data-token="${esc(name)}" value="${esc(colorHex(t.value))}">`
          : `<input class="full" type="text" data-token="${esc(name)}" value="${esc(t.value || "")}" placeholder="${esc(t.type)}">`;
      return `<div class="row"><label title="${esc(t.type)}">${esc(name)}</label>${control}<span class="val">${esc(t.type)}</span></div>`;
    })
    .join("");

  const otherThemes = (themes as any[]).filter((t) => !t.active && !t.error);
  const switcher = otherThemes.length
    ? `<h2 style="margin-top:1rem">Changer de thème</h2>
       <p class="hint">Une vérification de compatibilité (§5.3) précède toute bascule.</p>
       ${otherThemes
         .map(
           (t) =>
             `<div class="stack-item"><span class="name">${esc(t.name)} <span class="val">${esc(t.version)}</span></span>
              <button data-check="${esc(t.name)}">Vérifier</button>
              <button data-apply="${esc(t.name)}">Appliquer</button></div>`,
         )
         .join("")}`
    : "";

  panelEl.innerHTML = `
    <h2>Thème actif : ${esc(info.name)} <span class="val">${esc(info.version)}</span></h2>
    ${info.editable ? `<p class="hint">Les tokens s'aperçoivent en direct ; « Appliquer les tokens » réécrit tokens.css.</p>${tokenRows}
      <div style="margin-top:.6rem"><button id="tokens-save">Appliquer les tokens</button></div>`
      : `<p class="hint">Ce thème ne déclare pas de tokens.css : édition de tokens indisponible.</p>`}
    <div id="theme-switch">${switcher}</div>`;

  // Aperçu runtime instantané : on écrit la custom property sur :root, la page
  // se re-colore sans rebuild (§6). La persistance n'a lieu qu'à « Appliquer ».
  for (const inp of panelEl.querySelectorAll<HTMLInputElement>("[data-token]")) {
    inp.addEventListener("input", () => {
      document.documentElement.style.setProperty(inp.dataset.token!, inp.value);
    });
  }
  byId("tokens-save")?.addEventListener("click", saveTokens);
  for (const b of panelEl.querySelectorAll<HTMLButtonElement>("[data-check]")) {
    b.addEventListener("click", () => runThemeCheck(b.dataset.check!));
  }
  for (const b of panelEl.querySelectorAll<HTMLButtonElement>("[data-apply]")) {
    b.addEventListener("click", () => applyTheme(b.dataset.apply!));
  }
}

function colorHex(v: string): string {
  const s = (v || "").trim();
  return /^#[0-9a-fA-F]{6}$/.test(s) ? s : "#000000";
}

async function saveTokens(): Promise<void> {
  const values: Record<string, string> = {};
  for (const inp of panelEl.querySelectorAll<HTMLInputElement>("[data-token]")) {
    values[inp.dataset.token!] = inp.value;
  }
  setStatus("écriture des tokens…");
  const res = await fetch(`${API}/theme/tokens`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-Pal-CSRF": CFG.csrf },
    body: JSON.stringify(values),
  });
  if (res.ok) {
    setStatus("tokens appliqués", "saved");
  } else {
    setStatus("tokens refusés : " + (await res.text()).slice(0, 120), "error");
  }
}

async function runThemeCheck(name: string): Promise<void> {
  const rep = await fetch(`${API}/theme/check?theme=${encodeURIComponent(name)}`).then((r) => r.json());
  showReport(rep);
}

async function applyTheme(name: string): Promise<void> {
  if (!confirm(`Basculer vers le thème « ${name} » ? La compatibilité est vérifiée d'abord.`)) return;
  setStatus("bascule de thème…");
  const res = await fetch(`${API}/theme/apply`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Pal-CSRF": CFG.csrf },
    body: JSON.stringify({ theme: name }),
  });
  const rep = await res.json();
  if (res.status === 409) {
    showReport(rep);
    setStatus("bascule refusée : thème incompatible", "error");
    return;
  }
  if (res.ok) {
    setStatus("thème appliqué — rechargement…", "saved");
    setTimeout(() => location.reload(), 400);
  } else {
    setStatus("échec de la bascule", "error");
  }
}

function showReport(rep: any): void {
  const findings = (rep.findings || []) as any[];
  const body = findings.length
    ? findings.map((f) => `<div class="find ${esc(f.severity)}">[${esc(f.severity)}] ${esc(f.rule)} : ${esc(f.detail)}</div>`).join("")
    : `<p class="hint">Compatibilité totale, aucune remarque.</p>`;
  panelEl.innerHTML = `<h2>${esc(rep.current)} → ${esc(rep.candidate)}</h2>${body}
    <div style="margin-top:.6rem"><button id="report-back">Retour au thème</button></div>`;
  byId("report-back").addEventListener("click", renderThemePanel);
}

// ---- panneau SEO (§9/§11) : title, description, aperçu SERP ---------------------

function renderSEOPanel(): void {
  const m = CFG.meta || { title: "", description: "" };
  // « Suggérer » n'apparaît que si un fournisseur IA est configuré (§12).
  const suggestT = CFG.ai ? `<button id="seo-suggest-t" title="Proposer des titres (IA)">Suggérer</button>` : "";
  const suggestD = CFG.ai ? `<button id="seo-suggest-d" title="Proposer une description (IA)">Suggérer</button>` : "";
  panelEl.innerHTML = `
    <h2>Référencement — ${esc(CFG.page)}</h2>
    <div class="row"><label>Titre</label><input class="full" id="seo-title" type="text" value="${esc(m.title)}" maxlength="70"><span class="val" id="seo-title-n"></span></div>
    ${suggestT ? `<div class="row"><span></span><span>${suggestT}</span><span></span></div>` : ""}
    <div class="row"><label>Description</label><textarea id="seo-desc" maxlength="180">${esc(m.description)}</textarea><span class="val" id="seo-desc-n"></span></div>
    ${suggestD ? `<div class="row"><span></span><span>${suggestD}</span><span></span></div>` : ""}
    <div class="row"><label>og:image</label><input class="full" id="seo-og" type="text" value="${esc(m.ogImage || "")}" placeholder="media/… ou https://…"><span class="val"></span></div>
    <div class="serp">
      <div class="t" id="serp-t"></div><div class="u">${esc(location.origin)}${esc(location.pathname)}</div><div class="d" id="serp-d"></div>
    </div>
    <div id="ai-proposals"></div>
    <p class="hint">Titre ≤ 60, description 50–160 : au-delà, l'aperçu vire à l'ambre (mêmes règles que le lint §11).${CFG.ai ? " Les suggestions IA sont des propositions — rien n'est appliqué sans votre clic." : ""}</p>
    <button id="seo-save">Enregistrer le référencement</button>`;

  const titleEl = byId("seo-title") as HTMLInputElement;
  const descEl = byId("seo-desc") as HTMLTextAreaElement;
  const refresh = () => {
    const t = titleEl.value || CFG.page;
    const d = descEl.value;
    byId("serp-t").textContent = t;
    byId("serp-d").textContent = d;
    setCounter(byId("seo-title-n"), titleEl.value.length, 60);
    setCounter(byId("seo-desc-n"), descEl.value.length, 160, 50);
  };
  titleEl.addEventListener("input", refresh);
  descEl.addEventListener("input", refresh);
  refresh();
  byId("seo-save").addEventListener("click", saveSEO);

  // Suggestions IA (advisory-only) : on affiche les propositions, l'auteur
  // clique pour en adopter une — c'est ce clic qui remplit le champ, jamais l'IA.
  byId("seo-suggest-t")?.addEventListener("click", () =>
    suggest("title", (props) => showProposals(props, (p) => { titleEl.value = p; refresh(); })),
  );
  byId("seo-suggest-d")?.addEventListener("click", () =>
    suggest("description", (props) => showProposals(props, (p) => { descEl.value = p; refresh(); })),
  );
}

// suggest asks the assistant and hands the proposals to a renderer. It never
// writes anything itself (§12).
async function suggest(kind: string, onProposals: (props: string[]) => void, extra: Record<string, string> = {}): Promise<void> {
  setStatus("suggestion IA…");
  const res = await fetch(`${API}/ai/suggest`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Pal-CSRF": CFG.csrf },
    body: JSON.stringify({ kind, page: CFG.page, ...extra }),
  });
  if (!res.ok) {
    setStatus("suggestion refusée : " + (await res.text()).slice(0, 120), "error");
    return;
  }
  const data = await res.json();
  if (!data.proposals?.length) {
    setStatus("aucune proposition", "error");
    return;
  }
  setStatus("propositions prêtes — cliquez pour adopter", "saved");
  onProposals(data.proposals);
}

function showProposals(props: string[], onAdopt: (p: string) => void): void {
  const box = byId("ai-proposals");
  if (!box) return;
  box.innerHTML =
    `<p class="hint">Propositions (cliquez pour adopter) :</p>` +
    props.map((p, i) => `<div class="find" data-prop="${i}" style="cursor:pointer">${esc(p)}</div>`).join("");
  props.forEach((p, i) => {
    box.querySelector(`[data-prop="${i}"]`)?.addEventListener("click", () => {
      onAdopt(p);
      box.innerHTML = "";
      setStatus("proposition adoptée — pensez à enregistrer", "dirty");
    });
  });
}

function setCounter(el: Element, n: number, max: number, min = 0): void {
  el.textContent = String(n);
  (el as HTMLElement).style.color = n > max || (min > 0 && n > 0 && n < min) ? "#f6d32d" : "";
}

async function saveSEO(): Promise<void> {
  const body = {
    title: (byId("seo-title") as HTMLInputElement).value,
    description: (byId("seo-desc") as HTMLTextAreaElement).value,
    ogImage: (byId("seo-og") as HTMLInputElement).value,
  };
  setStatus("enregistrement du référencement…");
  const res = await fetch(`${API}/pages/${encodeURIComponent(CFG.page)}/meta`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-Pal-CSRF": CFG.csrf },
    body: JSON.stringify(body),
  });
  if (res.ok) {
    const saved = await res.json();
    CFG.meta = saved;
    setStatus("référencement enregistré", "saved");
  } else {
    setStatus("référencement refusé : " + (await res.text()).slice(0, 120), "error");
  }
}

// ---- panneau Vérifier (§11) : le lint à la demande ------------------------------

async function renderCheckPanel(): Promise<void> {
  panelEl.innerHTML = `<h2>Vérification qualité</h2><p class="hint">Analyse…</p>`;
  const rep = await fetch(`${API}/check`).then((r) => r.json());
  const issues = (rep.issues || []) as any[];
  const body = issues.length
    ? issues.map((i) => `<div class="find ${esc(i.severity)}">[${esc(i.severity)}] ${esc(i.page)} · ${esc(i.rule)} : ${esc(i.detail)}</div>`).join("")
    : `<p class="hint">Aucun problème détecté. Core Web Vitals par construction (§11).</p>`;
  panelEl.innerHTML = `<h2>Vérification qualité <span class="val">${rep.ms} ms</span></h2>${body}`;
}

// ---- panneau Historique (§13) : les révisions d'une région, restaurer ----------

async function renderHistoryPanel(): Promise<void> {
  const slot = state.activeSlot;
  if (!slot) {
    panelEl.innerHTML = `<h2>Historique</h2><p class="hint">Cliquez d'abord dans une région éditable, puis rouvrez l'historique.</p>`;
    return;
  }
  panelEl.innerHTML = `<h2>Historique — ${esc(slot)}</h2><p class="hint">Chargement…</p>`;
  const url = `${API}/history/${encodeURIComponent(CFG.page)}/${encodeURIComponent(slot)}`;
  const data = await fetch(url).then((r) => r.json());
  if (!data.enabled) {
    panelEl.innerHTML = `<h2>Historique — ${esc(slot)}</h2><p class="hint">Ce site n'est pas versionné (git) : pas d'historique.</p>`;
    return;
  }
  const revs = (data.revisions || []) as any[];
  const body = revs.length
    ? revs
        .map(
          (rv) =>
            `<div class="find" style="display:flex;gap:.5rem;align-items:center">
               <code style="opacity:.7">${esc(rv.hash.slice(0, 7))}</code>
               <span style="flex:1">${esc(rv.message)} <span class="val">${esc(rv.when)} · ${esc(rv.author)}</span></span>
               <button data-revert="${esc(rv.hash)}">Restaurer</button>
             </div>`,
        )
        .join("")
    : `<p class="hint">Aucune révision enregistrée pour cette région.</p>`;
  panelEl.innerHTML = `<h2>Historique — ${esc(slot)}</h2>
    <p class="hint">Restaurer réécrit la région par le chemin normal (re-sanitisée, commit dédié) — c'est une édition ordinaire, pas une réécriture de l'histoire.</p>${body}`;
  for (const b of panelEl.querySelectorAll<HTMLButtonElement>("[data-revert]")) {
    b.addEventListener("click", () => revertSlot(slot, b.dataset.revert!));
  }
}

async function revertSlot(slot: string, hash: string): Promise<void> {
  if (!confirm(`Restaurer « ${slot} » à la révision ${hash.slice(0, 7)} ?`)) return;
  setStatus("restauration…");
  const res = await fetch(`${API}/revert/${encodeURIComponent(CFG.page)}/${encodeURIComponent(slot)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Pal-CSRF": CFG.csrf },
    body: JSON.stringify({ hash }),
  });
  if (res.ok) {
    setStatus("région restaurée — rechargement…", "saved");
    setTimeout(() => location.reload(), 400);
  } else {
    setStatus("restauration refusée : " + (await res.text()).slice(0, 120), "error");
  }
}

// ---- éditeur de slot stack (§5.1/§9) : liste réordonnable + config de blocs -----

function initStackRegion(region: HTMLElement, slot: string): void {
  region.setAttribute("data-pal-stack", "true");
  paintRegion(region);
  const allowed = (CFG.slots[slot]?.blocks || []).filter((b) => !CFG.blocks[b]?.computed || b === "toc");
  const bar = document.createElement("div");
  bar.setAttribute("data-pal-ui", "true");
  bar.style.cssText = "display:flex;gap:.4rem;flex-wrap:wrap;margin:.4rem 0;font:13px system-ui;";
  bar.innerHTML =
    `<span style="opacity:.6">Ajouter un bloc :</span>` +
    allowed.map((b) => `<button type="button" data-add="${b}" style="cursor:pointer">${b}</button>`).join(" ");
  region.before(bar);

  const rerender = () => renderStackItems(region, slot);
  bar.querySelectorAll<HTMLButtonElement>("[data-add]").forEach((btn) => {
    btn.addEventListener("mousedown", (e) => e.preventDefault());
    btn.addEventListener("click", () => {
      const block = btn.dataset.add!;
      const el = document.createElement(CFG.blocks[block]?.elements?.[0] || "div");
      el.setAttribute("data-block", block);
      region.appendChild(el);
      markDirty(slot);
      rerender();
    });
  });
  rerender();
}

function renderStackItems(region: HTMLElement, slot: string): void {
  const items = [...region.querySelectorAll<HTMLElement>(":scope > [data-block]")];
  // Overlay de contrôle : réordonner / configurer / retirer, hors du fragment
  // stocké (les contrôles ne sont jamais sauvegardés).
  region.querySelectorAll("[data-pal-item]").forEach((n) => n.remove());
  items.forEach((item, i) => {
    const block = item.getAttribute("data-block") || "";
    const schema = CFG.blocks[block];
    const ui = document.createElement("div");
    ui.setAttribute("data-pal-item", "true");
    ui.setAttribute("data-pal-ui", "true");
    ui.className = "";
    ui.style.cssText = "font:13px system-ui;background:#eef;border:1px solid #99c;border-radius:5px;padding:.3rem .5rem;margin:.2rem 0;";
    const params = schema
      ? Object.entries(schema.params)
          .map(([p, spec]) => paramControl(item, block, p, spec))
          .join("")
      : "";
    ui.innerHTML =
      `<div style="display:flex;gap:.4rem;align-items:center">
         <strong style="flex:1">${esc(block)}</strong>
         <button type="button" data-up ${i === 0 ? "disabled" : ""}>↑</button>
         <button type="button" data-down ${i === items.length - 1 ? "disabled" : ""}>↓</button>
         <button type="button" data-del>Retirer</button>
       </div>${params}`;
    item.before(ui);
    ui.querySelector("[data-up]")?.addEventListener("click", () => {
      if (item.previousElementSibling) region.insertBefore(item, item.previousElementSibling);
      markDirty(slot); renderStackItems(region, slot);
    });
    ui.querySelector("[data-down]")?.addEventListener("click", () => {
      const next = item.nextElementSibling;
      if (next) region.insertBefore(next, item);
      markDirty(slot); renderStackItems(region, slot);
    });
    ui.querySelector("[data-del]")?.addEventListener("click", () => {
      item.remove(); markDirty(slot); renderStackItems(region, slot);
    });
    for (const ctrl of ui.querySelectorAll<HTMLInputElement | HTMLSelectElement>("[data-param]")) {
      ctrl.addEventListener("change", () => {
        item.setAttribute("data-" + ctrl.dataset.param!, ctrl.value);
        markDirty(slot);
      });
    }
  });
}

function paramControl(item: HTMLElement, _block: string, param: string, spec: ParamSchema): string {
  const cur = item.getAttribute("data-" + param) || "";
  let field = "";
  if (spec.kind === "enum") {
    field = `<select data-param="${esc(param)}">${(spec.values || [])
      .map((v) => `<option ${v === cur ? "selected" : ""}>${esc(v)}</option>`)
      .join("")}</select>`;
  } else if (spec.kind === "int") {
    field = `<input data-param="${esc(param)}" type="number" min="${spec.min ?? ""}" max="${spec.max ?? ""}" value="${esc(cur)}">`;
  } else {
    field = `<input data-param="${esc(param)}" type="text" value="${esc(cur)}">`;
  }
  return `<div class="cfg"><label>${esc(param)}</label>${field}</div>`;
}

// A stack region saves the fragment stripped of the overlay's own control UI.
function stackFragment(region: HTMLElement): string {
  const clone = region.cloneNode(true) as HTMLElement;
  clone.querySelectorAll("[data-pal-ui],[data-pal-item]").forEach((n) => n.remove());
  return clone.innerHTML;
}

// ---- sélecteur média : slots image (§9/§10) ------------------------------------

// Un slot image édite une <figure><img>. Cliquer ouvre un choix : téléverser un
// fichier (raster → variantes WebP+srcset, ou SVG → assaini) puis, une fois
// prêt, pointer l'image dessus. Le fragment stocké reste media/<chemin> ; le
// serveur canonicalise.
function initImageRegion(region: HTMLElement, slot: string): void {
  region.setAttribute("data-pal-image", "true");
  paintRegion(region);
  region.style.cursor = "pointer";
  region.title = "Cliquer pour changer l'image";
  region.addEventListener("click", (e) => {
    // Ne pas ré-ouvrir le sélecteur quand on clique le bouton « Suggérer un alt ».
    if ((e.target as HTMLElement).hasAttribute("data-pal-alt")) return;
    e.preventDefault();
    openMediaPicker((mediaPath) => {
      const altBtn = CFG.ai
        ? `<button type="button" data-pal-alt data-pal-ui style="margin-top:.3rem;cursor:pointer">Suggérer un alt (IA)</button>`
        : "";
      region.innerHTML = `<figure><img src="${esc(mediaPath)}" alt=""></figure>${altBtn}`;
      markDirty(slot);
      paintRegion(region);
      region.querySelector("[data-pal-alt]")?.addEventListener("click", (ev) => {
        ev.preventDefault();
        suggest("alt", (props) => {
          const img = region.querySelector("img");
          if (img && props[0]) {
            img.setAttribute("alt", props[0]);
            setStatus("alt adopté : « " + props[0].slice(0, 40) + " » — enregistrez", "dirty");
            markDirty(slot);
          }
        }, { src: mediaPath });
      });
    });
  });
}

function openMediaPicker(onPick: (mediaPath: string) => void): void {
  const input = document.createElement("input");
  input.type = "file";
  input.accept = "image/jpeg,image/png,image/webp,image/svg+xml";
  input.addEventListener("change", async () => {
    const file = input.files?.[0];
    if (!file) return;
    setStatus("téléversement…");
    const body = new FormData();
    body.append("file", file);
    const res = await fetch(`${API}/media`, {
      method: "POST",
      headers: { "X-Pal-CSRF": CFG.csrf },
      body,
    });
    if (!res.ok) {
      setStatus("téléversement refusé : " + (await res.text()).slice(0, 120), "error");
      return;
    }
    const data = await res.json();
    if (data.original) {
      // SVG : réponse synchrone, chemin immédiat.
      onPick(data.original);
      setStatus("image ajoutée", "saved");
    } else if (data.id) {
      // Raster : la file travaille en asynchrone ; on attend l'évènement media.
      setStatus("encodage de l'image…");
      pendingMedia.set(data.id, onPick);
    }
  });
  input.click();
}

const pendingMedia = new Map<string, (path: string) => void>();

// ---- grille tableur : slots data (§3.3/§9) -------------------------------------

function initDataRegion(region: HTMLElement, slot: string): void {
  region.setAttribute("data-pal-data", "true");
  paintRegion(region);
  region.style.cursor = "pointer";
  region.title = "Cliquer pour éditer la table";
  region.addEventListener("click", (e) => {
    e.preventDefault();
    const source = region.getAttribute("data-source") || slot;
    openDataGrid(source);
  });
}

async function openDataGrid(table: string): Promise<void> {
  togglePanel("data:" + table, async () => {
    panelEl.innerHTML = `<h2>Table « ${esc(table)} »</h2><p class="hint">Chargement…</p>`;
    const data = await fetch(`${API}/data/${encodeURIComponent(table)}`).then((r) => r.json());
    renderDataGrid(table, data);
  });
}

function renderDataGrid(table: string, data: any): void {
  const header: string[] = data.header?.length ? data.header : Object.keys(data.schema || {});
  const rows: string[][] = data.rows || [];
  const schema: Record<string, string> = data.schema || {};

  const head = header.map((h) => `<th>${esc(h)} <span class="val">${esc(schema[h] || "")}</span></th>`).join("");
  const body = rows
    .map(
      (row, i) =>
        `<tr data-row="${i}">${header
          .map((h, j) => `<td><input data-col="${j}" value="${esc(row[j] || "")}"></td>`)
          .join("")}<td><button data-del-row="${i}" title="Supprimer la ligne">✕</button></td></tr>`,
    )
    .join("");

  panelEl.innerHTML = `
    <h2>Table « ${esc(table)} » <span class="val">${schema ? Object.keys(schema).length : 0} colonne(s)</span></h2>
    <p class="hint">Validée à l'enregistrement contre le schéma du thème (§3.3). Cellule vide = donnée absente, autorisée.</p>
    <div style="overflow:auto"><table style="border-collapse:collapse;width:100%">
      <thead><tr>${head}<th></th></tr></thead><tbody id="grid-body">${body}</tbody>
    </table></div>
    <div style="margin-top:.6rem;display:flex;gap:.5rem">
      <button id="grid-add">+ Ligne</button>
      <button id="grid-save">Enregistrer la table</button>
    </div>`;

  byId("grid-add").addEventListener("click", () => {
    const empty = header.map(() => "");
    rows.push(empty);
    renderDataGrid(table, { header, rows, schema });
  });
  for (const b of panelEl.querySelectorAll<HTMLButtonElement>("[data-del-row]")) {
    b.addEventListener("click", () => {
      rows.splice(Number(b.dataset.delRow), 1);
      renderDataGrid(table, { header, rows, schema });
    });
  }
  // Keep the in-memory rows synced with edits.
  for (const inp of panelEl.querySelectorAll<HTMLInputElement>("[data-col]")) {
    inp.addEventListener("input", () => {
      const tr = inp.closest("tr")!;
      const i = Number(tr.getAttribute("data-row"));
      const j = Number(inp.dataset.col);
      rows[i][j] = inp.value;
    });
  }
  byId("grid-save").addEventListener("click", () => saveDataGrid(table, header, rows));
}

async function saveDataGrid(table: string, header: string[], rows: string[][]): Promise<void> {
  setStatus("enregistrement de la table…");
  const res = await fetch(`${API}/data/${encodeURIComponent(table)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-Pal-CSRF": CFG.csrf },
    body: JSON.stringify({ header, rows }),
  });
  if (res.ok) {
    setStatus("table enregistrée", "saved");
  } else {
    setStatus("table refusée : " + (await res.text()).slice(0, 140), "error");
  }
}

// ---- publication (§13) : bouton, si le site la déclare --------------------------

async function publish(): Promise<void> {
  if (!confirm("Publier le site vers son dépôt distant ?")) return;
  setStatus("publication…");
  const res = await fetch(`${API}/publish`, { method: "POST", headers: { "X-Pal-CSRF": CFG.csrf } });
  if (res.ok) {
    const r = await res.json();
    setStatus(`publié → ${r.remote}/${r.branch} (${r.detail})`, "saved");
  } else {
    setStatus("publication échouée : " + (await res.text()).slice(0, 140), "error");
  }
}

// ---- flux d'évènements (SSE, §8 : builds, erreurs, reload, média) ---------------

const events = new EventSource(API + "/events");
events.addEventListener("reload", () => {
  if (state.saving || state.dirty.size > 0 || Date.now() < state.ignoreReloadUntil) return;
  location.reload();
});
events.addEventListener("build", (e) => {
  try {
    const b = JSON.parse((e as MessageEvent).data);
    setStatus(`enregistré — page régénérée en ${b.ms} ms`, "saved");
  } catch {
    /* un build illisible n'est pas une erreur d'édition */
  }
});
events.addEventListener("media", (e) => {
  try {
    const ev = JSON.parse((e as MessageEvent).data);
    if (ev.stage) {
      setStatus("image : " + ev.stage);
    } else if (ev.result) {
      const cb = pendingMedia.get(ev.id);
      if (cb) {
        pendingMedia.delete(ev.id);
        cb(ev.result.original);
      }
      setStatus("image ajoutée", "saved");
    } else if (ev.error) {
      setStatus("image refusée : " + ev.error, "error");
    }
  } catch {
    /* évènement média illisible : ignoré */
  }
});
events.addEventListener("error", (e) => {
  const data = (e as MessageEvent).data;
  if (typeof data === "string" && data) {
    console.error("palimpseste:", data);
    setStatus("incident serveur — voir la console", "error");
  }
  // Sans data : simple reconnexion EventSource, rien à faire.
});
