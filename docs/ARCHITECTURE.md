# Palimpseste — Architecture

> *Nom de code provisoire — un palimpseste est un parchemin gratté puis réécrit : des pages
> re-matérialisées indéfiniment au-dessus d'un contenu qui, lui, persiste.*

**Révision 0.2 — 2026-06-12.** Journal des révisions en fin de document.

**En une phrase :** un CMS à édition in-place dans l'esprit de Sitecake, livré comme un unique
binaire Go, dont le contenu vit en fragments HTML sémantiques et dont les pages publiées ne sont
que des vues matérialisées — `f(fragments, data, thème) → site statique`, déterministe et
attestable.

---

## 1. Philosophie et principes fondateurs

1. **La page est l'interface.** Pas de back-office : on édite le site en le regardant, dans son
   rendu final. L'héritage de Sitecake, conservé intégralement côté expérience.
2. **Inversion du stockage.** Là où Sitecake fusionnait contenu et présentation dans le HTML
   final, le contenu est ici la source de vérité, stocké en fragments séparés ; les pages
   publiées sont des artefacts jetables, régénérables.
3. **Le backend est déplacé dans le temps.** Un backend complet existe — mais seulement pendant
   l'édition. En production : HTML statique pur, surface d'attaque nulle, rien ne s'exécute,
   rien à patcher. Conséquence assumée : tout dynamisme côté visiteur est soit précalculé au
   build, soit délégué (formulaires → service tiers ou micro-endpoint volontairement séparé).
4. **La souplesse est une propriété du système, pas du langage.** Cœur rigide (Go, compilé),
   points d'extension formels (blocs déclarés, plus tard WASM). Aucune exécution de code
   arbitraire, nulle part.
5. **La portabilité s'achète par la contrainte.** Contrats stricts sur le contenu (§4), les
   données (§3.3) et les thèmes (§5) ⇒ thèmes interchangeables sans réécrire une page.
6. **Déterminisme de bout en bout.** Mêmes entrées + même binaire = artefact identique bit à
   bit. La matérialisation est une fonction pure, donc attestable — et donc mémoïsable (§15).
7. **Zéro base de données, zéro runtime externe.** Des fichiers plats, un dépôt git, un binaire.
8. **La performance est un invariant, pas une optimisation a posteriori.** Budgets chiffrés
   (§15), tenus par des tests, dès M0.
9. **L'intelligence n'intervient qu'au moment d'éditer, jamais au build.** L'IA propose,
   l'humain committe, la matérialisation reste pure (§12).

---

## 2. Vue d'ensemble

### 2.1 Les trois modes du binaire

| Mode | Commande | Rôle |
|---|---|---|
| **build** | `palimpseste build [--check]` | Matérialise `content/ + data/ + theme/ → public/`. One-shot, scriptable, CI-compatible. `--check` ajoute le lint SEO/qualité (§11). |
| **edit** | `palimpseste edit [--listen]` | Serveur d'édition : sert les pages matérialisées, superpose l'éditeur, expose l'API. Local (loopback, sans auth) ou distant (auth admin). |
| **serve** | `palimpseste serve` | Serveur statique de confort pour `public/`. Optionnel — n'importe quel hébergeur fait l'affaire. |

### 2.2 Flux de données

```
                        ÉDITION                                   PRODUCTION
        ┌────────────────────────────────────┐
        │  Navigateur                        │
        │  page rendue + overlay (Shadow DOM)│
        └────────────┬───────────▲───────────┘
                     │ PUT/POST  │ SSE (reload)
        ┌────────────▼───────────┴───────────┐
        │  Binaire Go (mode edit)            │
        │  sanitisation ─ écriture fragment  │
        │  commit go-git ─ re-matérialisation│
        └───────┬─────────┬─────────┬────────┘
                │         │         │
        ┌───────▼──┐ ┌────▼────┐ ┌──▼──────────┐       ┌──────────────────┐
        │ content/ │ │ data/   │ │ themes/     │ build │ public/  (HTML   │ ──► hébergement
        │ fragments│ │ tables  │ │ <actif>     │ ────► │ statique pur)    │     statique
        │ (vérité) │ │ (vérité)│ │ templates   │       │                  │     quelconque
        └───────┬──┘ └────┬────┘ └─────────────┘       └──────────────────┘
                │         │
        ┌───────▼─────────▼───┐
        │ dépôt git           │  (historique = undo ; push = publication)
        └─────────────────────┘
```

### 2.3 Deux workflows assumés

- **Local-first (designer)** : le binaire tourne sur le poste, on édite sur `localhost`, chaque
  sauvegarde est un commit, `git push` déclenche le déploiement (CI ou Pages).
- **Serveur (client final)** : le binaire tourne sur le serveur en mode `edit --listen`,
  authentifié ; il régénère `public/` à chaque sauvegarde. Le site servi reste statique.

---

## 3. Arborescence et modèle de données

```
site/
├── site.json                  # config : routes, métadonnées, thème actif, publication
├── site.lock                  # attestation : hashs contenu + data + thème@version + binaire
│                              #   + blobs WASM épinglés (libwebp…)
├── content/
│   ├── _global/               # fragments partagés (nav, footer, sidebar, logo…)
│   └── <page>/<slot>.html
├── data/                      # tables : la vérité tabulaire (§3.3)
│   ├── equipe.csv
│   └── tarifs.csv
├── themes/
│   └── <theme>/
│       ├── theme.json         # manifeste : slots, blocs, tables, tokens, templates
│       ├── templates/*.html
│       ├── css/{theme,tokens}.css
│       └── assets/
├── media/
│   ├── originals/             # uploads (orientation cuite, métadonnées purgées)
│   └── derived/               # variantes WebP + og-jpeg (cache, régénérable)
└── public/                    # ARTEFACT — jamais édité à la main
    └── → builds/<hash>/       # symlink : bascule atomique, rollback trivial
```

### 3.1 `site.json` — le manifeste du site

```json
{
  "name": "Mon site",
  "lang": "fr",
  "theme": "encre",
  "meta": { "titlePattern": "%s — Mon site" },
  "routes": {
    "/":        { "template": "home", "content": "accueil", "title": "Accueil" },
    "/contact": { "template": "page", "content": "contact", "title": "Contact" }
  },
  "publish": { "method": "git-push", "remote": "origin", "branch": "pages" }
}
```

Scope V1 : pages statiques + fragments globaux. Collections (blog, pagination, flux) : V2 —
l'arborescence les accueille, le moteur pas encore. i18n : V2+, structure prête
(`content/<lang>/`). **Aucun secret dans `site.json`** (committé) : clés et endpoints (IA,
déploiement) vivent en variables d'environnement ou dans
`$XDG_CONFIG_HOME/palimpseste/config.toml`, hors dépôt.

### 3.2 Identité des fragments

Un fragment = `content/<page>/<slot>.html`, ou `content/_global/<slot>.html` pour les slots
partagés. Le nom de slot est l'identifiant pivot : déclaré par les thèmes, référencé par les
templates, ciblé par l'API, suivi par le graphe de dépendances du build incrémental.

### 3.3 Le contrat de données (`data/`)

La question « base de données » se coupe en deux : un **format de vérité** et un **moteur de
requête**. La philosophie impose le premier — la vérité reste des fichiers plats, diffables,
committables (un diff CSV se lit par un humain ; un diff de fichier binaire est du bruit qui
casserait l'undo lisible et le récit d'attestation). Le moteur est un détail du build.

- **Format** : CSV pour l'esprit tables (grille, colonnes, diff ligne à ligne), JSON pour les
  enregistrements imbriqués. Versionnés dans git comme le contenu.
- **Schéma** : déclaré par le thème dans `theme.json` (§5.1), validé de manière autoritaire
  côté serveur à chaque écriture — le bluemonday des données.
- **Édition** : micro-éditeur en grille (type tableur) dans l'overlay.
- **Moteur** : à l'échelle d'un site, la table est un `[]struct` en mémoire au build — tri,
  filtre, groupage en Go pur, déterministe. Aucun moteur embarqué. Option de compilation
  `-tags duckdb` pour l'ergonomie SQL sur fichiers (CGo, même statut que `media_vips`).
- **Écartés comme vérité** : KV purs Go (bbolt, Badger, Pebble) et documents embarqués
  (CloverDB) — binaires donc git-hostiles ; place légitime éventuelle : cache interne du
  binaire, jamais le stockage du site. Si du relationnel embarqué devenait nécessaire :
  `modernc.org/sqlite` (pur Go, zéro CGo) sans trahir le binaire unique.
- **Patterns dérivés** (gros volumes interrogeables côté client, runtime toujours nul) : index
  de recherche précalculé façon Pagefind ; ou SQLite **cuit comme artefact statique** au build,
  requêté dans le navigateur via WASM et requêtes HTTP par plages d'octets (sql.js-httpvfs).

> On n'importe pas une base de données — on importe un contrat de données.

---

## 4. Le contrat de contenu

Le contenu est du **HTML sémantique contraint**, appliqué de manière **autoritaire côté
serveur** (bluemonday, politique whitelist) à chaque sauvegarde — le nettoyage côté éditeur
n'est qu'un confort. Toute écriture passe par cette porte, **y compris celles issues de
l'assistant IA** (§12) : le sanitiseur est aussi le garde-fou de l'IA.

**Vocabulaire autorisé :**

| Catégorie | Éléments |
|---|---|
| Structure | `h2 h3 h4 p blockquote hr` |
| Listes | `ul ol li` |
| Inline | `a strong em code br` |
| Médias | `figure figcaption img` (`src` relatif vers `media/`, `alt` requis) |
| Code | `pre code` |
| Blocs nommés | conteneurs porteurs de `data-block` (§4.1) |

**Règles de stripping :** suppression de `class`, `style`, `id`, handlers, `data-*` inconnus
(survivent : `href`, `src`, `alt`, `title`, `data-block` et ses paramètres déclarés) ; `h1`
interdit (il appartient au template) ; URLs média relatives ; `javascript:` et data-URIs
rejetés ; collage Word/Docs normalisé agressivement.

### 4.1 Les blocs nommés — le point d'extension V1

Un bloc est une **structure sémantique conventionnelle**, pas du code. Ses paramètres sont des
attributs `data-*` **whitelistés par type**, déclarés au catalogue avec leur schéma (type,
bornes, valeurs énumérées) et validés à l'écriture :

```html
<figure data-block="gallery">…</figure>
<aside  data-block="recent" data-source="blog" data-count="5"></aside>
<div    data-block="table"  data-source="equipe"></div>
```

**Catalogue V1** — deux familles :

- *Statiques* : `gallery`, `columns`, `cta`, `embed` (iframe sandboxée, liste blanche de
  domaines).
- *Computés au build* : `table` (rend une table de `data/`), `toc` (table des matières de la
  page) ; `recent` arrive avec les collections V2. Built-ins du binaire d'abord ; la frontière
  WASM (§16) prendra le relais pour les blocs computés tiers.

Un bloc non supporté par le thème reste du HTML sémantique valide — dégradation élégante,
jamais de page cassée.

---

## 5. Le contrat de thème

Un thème est **de la donnée, pas du code** : templates HTML pur, CSS, manifeste. Aucun langage
de templates — l'injection se fait au niveau du DOM, par le binaire.

### 5.1 `theme.json` — le manifeste

```json
{
  "name": "encre",
  "version": "1.3.0",
  "slots": {
    "_global:nav":     { "type": "nav" },
    "_global:logo":    { "type": "image", "formats": ["vector"], "inline": true },
    "_global:sidebar": { "type": "stack", "blocks": ["recent", "table", "cta", "embed"] },
    "_global:footer":  { "type": "richtext" },
    "hero.title":      { "type": "plain", "maxLength": 120 },
    "main":            { "type": "richtext", "blocks": ["gallery", "columns", "cta", "table"] }
  },
  "data": {
    "equipe": { "format": "csv",
                "columns": { "nom": "string", "role": "string", "photo": "media" } }
  },
  "templates": { "home": "templates/home.html", "page": "templates/page.html" },
  "tokens": {
    "--pico-primary":       { "type": "color",  "snap": "open-props" },
    "--pico-border-radius": { "type": "radius" },
    "--font-heading":       { "type": "font" }
  }
}
```

**Types de slots** (chaque type pilote un micro-éditeur dédié) :

| Type | Contenu | Éditeur |
|---|---|---|
| `plain` | texte nu, une ligne | champ inline |
| `richtext` | vocabulaire complet du §4 | contenteditable + toolbar |
| `stack` | pile de blocs, sans prose libre | liste réordonnable + panneau de config par bloc |
| `image` | une `figure` unique (raster et/ou vector) | sélecteur média |
| `nav` | liste de liens structurée | éditeur de menu |

`stack` est la réponse au besoin « widgets » : *widget zone = slot, widget = bloc* — WordPress
lui-même a conclu ainsi en 5.8. Pas de troisième primitive, un seul catalogue, même
sanitisation ; l'UI peut dire « widgets », l'ontologie reste à deux concepts. Stockage
inchangé : un fragment HTML, séquence de `data-block`.

### 5.2 Templates : HTML pur + `data-slot`

```html
<body>
  <nav data-slot="_global:nav"></nav>
  <header class="hero"><h1 data-slot="hero.title"></h1></header>
  <div class="layout">
    <article data-slot="main"></article>
    <aside data-slot="_global:sidebar"></aside>
  </div>
  <footer data-slot="_global:footer"></footer>
</body>
```

La contrainte sémantique ne s'applique qu'au contenu, jamais au thème.

### 5.3 Validation de compatibilité — *avant* application

`palimpseste theme check <nom>` (et avant tout `theme apply`) : slot requis absent → erreur
bloquante ; slot offert sans contenu → avertissement + fragment vide ; bloc non déclaré →
avertissement (dégradation §4.1) ; **schéma `data` divergent → rapport de migration** ;
renommages → table `"migrate"` optionnelle, appliquée comme commit dédié.

---

## 6. CSS et theming

1. **CSS natif moderne comme langage d'écriture** — custom properties, nesting natif,
   `oklch()`, `color-mix()`, `clamp()`, `light-dark()`, ordonné en `@layer`. **Aucun
   préprocesseur** : les variables Sass meurent à la compilation ; les custom properties vivent
   au runtime — condition de l'édition de thème en temps réel.
2. **Open Props = dictionnaire de tokens** : les contrôles de l'éditeur s'aimantent à ses
   gammes — on contraint les choix à des choix harmonieux.
3. **Pico CSS v2 = thème par défaut** (style le HTML sémantique nu ; son normalize, pas celui
   d'Open Props).
4. **Le pont** :

```css
@layer reset, tokens, elements, theme;
@import "vendor/open-props.css" layer(tokens);
@import "vendor/pico.css"       layer(elements);
@import "tokens.css"            layer(theme);    /* ← réécrit par l'éditeur */

@layer theme {
  :root { --pico-primary: var(--indigo-6); --pico-border-radius: var(--radius-2); }
  /* styles des blocs déclarés : [data-block="gallery"] { … } */
}
```

5. **Passe d'optimisation au build** : esbuild en bibliothèque, in-process. Option différée :
   Lightning CSS en WASM via wazero — même porte que le reste (§16).
6. **Édition en direct** : pickers liés aux tokens (aperçu runtime instantané), persistance de
   `tokens.css` seul — re-thémer ne touche ni contenu ni pages.
7. **Synergie logo** : le logo SVG inliné (§10.2) avec `fill="currentColor"` hérite des tokens —
   il se recolore avec le thème et `light-dark()` sans réexport.

---

## 7. Le moteur de matérialisation

Cœur du système, **premier jalon livré** (M0), testable isolément.

**Pipeline, par page :** parse du template (`x/net/html`, jamais de regex) → injection des
fragments par `data-slot` (sous-arbres parsés) → rendu des blocs computés (`table`, `toc`) →
inlining des assets `inline:true` (logo, profil strict §10.2) → réécriture des URLs d'assets en
noms **adressés par contenu** (`app-3f2a91.css` : cache-busting et déterminisme d'un même
geste) → passe esbuild → minification HTML (`tdewolff/minify`) → sérialisation dans
`builds/<hash>/` → **bascule atomique du symlink `public/`** (rollback = re-pointer ; échos
d'ABRoot appliqués à un site).

**Sorties SEO générées ici** (déterministes) : `sitemap.xml`, `robots.txt`, canoniques,
JSON-LD (`Organization`, `WebSite` ; `Article` avec les collections V2), balises OG/Twitter —
`og:image` servi par la variante JPEG dédiée (§10.1).

**Incrémental & parallèle :** graphe de dépendances slot/table → pages (`_global:* → toutes`) ;
pages matérialisées en parallèle (pool de goroutines borné) ; **mémoïsation par
content-addressing** — une page dont le tuple d'entrées (hashs des fragments, du template, des
tokens, des tables) n'a pas changé n'est pas reconstruite. *Le déterminisme paie sa propre
performance : une fonction pure est mémoïsable par définition.*

**Attestation :** `site.lock` consigne hashs de `content/`, `data/`, thème@version, binaire, et
les blobs WASM épinglés (libwebp…). Quiconque détient les entrées reproduit `public/` bit à
bit — dérivation vérifiable, images dérivées comprises.

---

## 8. Serveur d'édition et API

HTTP, stdlib `net/http`. En mode `edit`, le serveur sert les pages matérialisées en y injectant
l'overlay ; tout le reste passe par l'API.

```
GET    /api/pages
GET    /api/fragments/{page}/{slot}
PUT    /api/fragments/{page}/{slot}        # sanitisation → écriture → commit → build incrémental
GET    /api/data/{table}
PUT    /api/data/{table}                   # validation de schéma → écriture → commit → build
POST   /api/media                          # magic bytes, orientation cuite, strip, variantes
GET    /api/theme            PUT /api/theme/tokens
GET    /api/themes           POST /api/theme/apply     # check §5.3 puis bascule
GET    /api/check                          # rapport lint SEO/qualité (§11)
POST   /api/ai/suggest                     # {kind: alt|description|title, cible} → propositions
                                           #   JAMAIS d'écriture directe : l'humain valide (§12)
GET    /api/history          POST /api/revert
POST   /api/publish                        # git push / rsync selon site.json
GET    /api/events                         # SSE : builds, progression médias, erreurs
```

**Auth** : local = bind `127.0.0.1`, sans auth. Distant = mono-admin V1, argon2id
(`x/crypto/argon2`), cookie `HttpOnly/Secure/SameSite=Strict`, CSRF sur toute mutation,
rate-limit login. Multi-utilisateurs : V2.

**Live reload** : fsnotify sur `content/`, `data/`, `themes/` + SSE.

---

## 9. L'éditeur (overlay front)

- **Vanilla TypeScript, zéro framework**, embarqué via `embed.FS` ; **Shadow DOM** intégral —
  le CSS du thème et celui de l'éditeur ne peuvent se contaminer dans aucun sens.
- Micro-éditeurs par type de slot (§5.1) : contenteditable contraint au contrat pour
  `richtext` (l'UI ne propose jamais ce que le serveur refuserait) ; **liste réordonnable +
  panneaux de configuration générés depuis le schéma des `data-*`** pour `stack` ; **grille
  type tableur** pour les tables `data/` ; éditeur de menu pour `nav`.
- **Panneau SEO par page** : title et description avec compteurs et aperçu SERP, choix de
  l'`og:image`.
- **Boutons « Suggérer »** contextuels (alt d'image, description, titre) si un fournisseur IA
  est configuré (§12) — propositions affichées, jamais appliquées sans geste humain.
- Panneau thème : contrôles générés depuis `theme.json#tokens`, aperçu runtime, persistance.
- Sauvegarde : `PUT` → le serveur renvoie le fragment canonique post-sanitisation, qui
  **remplace** le DOM édité — l'auteur voit toujours exactement ce qui est stocké.

---

## 10. Pipeline média

### 10.1 Raster

**Ordre du pipeline — décoder → cuire l'orientation EXIF dans les pixels → redimensionner →
encoder.** Les métadonnées ne survivent pas au réencodage : purge garantie par construction
(et zéro timestamp embarqué : la reproductibilité y gagne). Validation par magic bytes à
l'upload ; le réencodage neutralise les polyglottes.

- **Format de livraison : WebP** — lossy pour les photos, lossless pour graphiques et captures
  (remplace le PNG), alpha géré dans les deux cas. Variantes responsive 480/800/1200 +
  `srcset`.
- **Une exception pragmatique** : une variante **JPEG par image pour l'`og:image`** (certains
  scrapers sociaux boudent encore le WebP). Tout le reste : WebP pur.
- **AVIF** : option premium (encodage lent), jamais imposée au build par défaut.
- **Encodage sans CGo : libwebp compilé en WASM, exécuté via wazero** (approche
  `gen2brain/webp`). Binaire unique préservé ; ~2× plus lent que le natif — indolore pour un
  travail amorti et mis en cache (`media/derived/`). Le blob WASM est épinglé par hash dans
  `site.lock`. Option `-tags media_vips` (CGo) pour vitesse native + AVIF rapide.
- Génération **asynchrone** (file de travail, progression via SSE) : l'upload ne bloque jamais
  l'éditeur. Décodage/resize pur Go (`disintegration/imaging`).

### 10.2 SVG — un document, pas une image

Le SVG est le format le plus *palimpsestien* qui soit — texte, diffable, committable,
thémable — mais c'est un **document XML** : `<script>`, handlers `on*`, `<foreignObject>`,
références externes, SMIL. Vecteur sournois : la **navigation directe** vers
`…/media/logo.svg` exécute ses scripts dans l'origine du site. Et l'on ne contrôle pas les
en-têtes HTTP d'un hébergeur statique arbitraire — pas de CSP de secours : **la sanitisation
porte la garantie seule.**

- **Sanitiseur maison par liste blanche** sur le flux de tokens `encoding/xml` (~200 lignes de
  cœur de métier ; pas de DOMPurify mature en Go). Le parseur Go ignore les entités externes —
  l'XXE meurt à la porte. Éléments géométriques, gradients, defs, `use` restreint aux
  références locales `#…` ; scripts, handlers, `foreignObject`, URLs externes : rayés.
- **Re-sérialisation depuis l'arbre nettoyé** — jamais les octets d'origine. Tue les
  polyglottes (le SVG n'a pas de magic bytes), produit une forme canonique déterministe et
  joliment diffable. Minification `tdewolff/minify` au build. `viewBox` imposé (injecté s'il
  manque) pour le dimensionnement CSS des thèmes.
- **Deux profils** : `img` (servi via `<img>`, contexte sans exécution, liste blanche
  standard) ; `inline`, strict, réservé aux assets que la matérialisation **incruste dans le
  HTML**.
- **Le logo** : slot `_global:logo` (`formats: ["vector"], inline: true`), inliné avec
  `fill="currentColor"` → il hérite des tokens, se recolore avec le thème et `light-dark()`
  sans réexport depuis Illustrator. Le binaire en dérive le **jeu de favicons** (SVG natif +
  PNG de compatibilité, Apple touch).
- Le SVG court-circuite le raster : pas de variantes, pas de `srcset`.

---

## 11. SEO et qualité

Palimpseste est **structurellement favorisé** : Core Web Vitals parfaits par construction
(statique, zéro JS par défaut sur les pages publiées, LCP instantané) et un contrat de contenu
qui produit exactement le balisage récompensé — hiérarchie de titres imposée, `alt` requis,
HTML sémantique sans soupe de divs.

- **Au build (déterministe)** : sitemap, robots, canoniques, JSON-LD, OG/Twitter (§7).
- **À l'édition** : panneau meta par page (title/description = slots `plain` avec compteurs et
  aperçu SERP, sélection `og:image`).
- **Le lint — la vraie killer feature, sans IA** : `palimpseste build --check` (et
  `GET /api/check`) — titres/descriptions absents ou hors gabarit, `alt` vides, liens internes
  cassés, hiérarchie de titres sautée, images orphelines. Des règles, du déterminisme, dans le
  cœur. Sortie machine-lisible → CI peut échouer sur régression.

---

## 12. Assistant IA (optionnel)

**Règle inviolable : jamais dans le build, seulement au moment d'éditer.** Une IA dans le
pipeline tuerait la reproductibilité ; une IA dans l'overlay n'est qu'un assistant de saisie.
L'IA propose → l'humain valide → commit ordinaire → matérialisation pure, intacte.

- **Forme** : aucun modèle, aucun runtime d'inférence dans le binaire — un simple client HTTP
  parlant le **dialecte OpenAI-compatible**, endpoint et clé configurables (env ou
  `config.toml` hors dépôt, jamais dans `site.json`). Couvre d'un même geste les API distantes
  et **Ollama en local** (`localhost:11434` : rien ne quitte la machine — argument de
  confidentialité pour les agences). Non configuré, la fonctionnalité **n'existe pas**.
- **Cibles V1, par valeur décroissante** : texte alternatif des images (modèle vision —
  accessibilité et SEO d'un même geste, la corvée que tout le monde saute) ; brouillons de meta
  descriptions depuis les fragments de la page (input LLM idéal : HTML propre) ; variantes de
  titres. Horizon V2-i18n : traduction assistée.
- **Jamais d'automation** : pas de génération auto-publiée (les politiques anti-spam de Google
  ciblent le contenu généré à l'échelle ; assistance, pas automation).
- **Garde-fou structurel** : toute écriture issue de l'IA passe par la même sanitisation que la
  saisie humaine (§4) — même victime d'une injection de prompt, le modèle ne peut rien écrire
  que le contrat n'autorise.

---

## 13. Versionnage et publication

- **go-git** : chaque sauvegarde = un commit (auteur = utilisateur, message structuré
  `edit(accueil/main)`, `data(equipe)`, `theme(tokens)`). Historique et undo *gratuits* ; l'UI
  d'historique n'est qu'une vue sur `git log`. (go-git plus lent que le git C sur de très gros
  dépôts — sans conséquence à l'échelle d'un site ; repli possible vers le binaire git si un
  jour pathologique.)
- `builds/` committé ou non selon stratégie (branche `pages` pour Codeberg Pages, ou CI qui
  exécute `build`).
- **Publication** = acte explicite (`POST /api/publish`) — découplée de *sauvegarder*.
- Secrets (clés IA, tokens de déploiement) : **jamais dans le dépôt**.

---

## 14. Sécurité — modèle de menace

| Menace | Réponse |
|---|---|
| XSS stocké (contenu) | bluemonday whitelist, autoritaire serveur, à chaque écriture |
| SVG hostile (script, foreignObject, navigation directe) | sanitiseur XML maison + re-sérialisation canonique + profils `img`/`inline` (§10.2) |
| Injection via données tabulaires | schéma typé validé serveur ; rendu des tables par le binaire (échappement systématique) |
| Injection de prompt → écriture hostile | sortie IA soumise à la même sanitisation + validation humaine obligatoire (§12) |
| XSS via thème | thèmes = donnée ; liste blanche d'éléments de template, pas de `<script>` V1 |
| CSRF | jeton sur toute mutation + `SameSite=Strict` |
| Path traversal | identifiants `page/slot/table` validés par regex stricte, résolution confinée sous `site/` |
| Upload hostile / polyglottes | magic bytes, taille max, **réencodage systématique** (raster) / re-sérialisation (SVG) |
| Bruteforce login | argon2id + rate-limit + verrouillage progressif |
| Exécution de code | impossible par construction : aucun plugin, aucun template Turing-complet, aucune éval |
| Fuite de secrets | clés hors dépôt (env / config.toml), exclues des commits |
| Production | surface nulle : fichiers statiques, le binaire n'y tourne pas |

---

## 15. Performance — budgets et invariants

La performance n'est pas une qualité à conquérir : c'est un **sous-produit de l'architecture**,
protégé par des budgets tenus en CI. Profil de charge : **O(éditions), pas O(visiteurs)** — le
travail coûteux n'arrive qu'aux moments où un humain agit ; le serveur de production dort
(fichiers statiques : `sendfile` noyau, CDN-cacheable ; un VPS minimal ou un Raspberry Pi
suffisent).

**Budgets (tests de non-régression, échec CI si dépassés) :**

| Opération | Budget |
|---|---|
| Cycle sauvegarde → page régénérée (bout en bout) | **< 100 ms** |
| Matérialisation complète | classe **~1 ms/page** (Hugo = preuve d'existence du genre) |
| Passe CSS (esbuild in-process) | quelques ms |
| Binaire au repos (mode edit) | **15–30 Mo RAM**, 0 % CPU |
| Variantes image (WebP/wazero) | asynchrone, jamais bloquant ; ~2× le natif, caché |

**Moyens (et leur principe directeur) :**

- **Le déterminisme paie sa propre performance** : la fonction de build étant pure, le
  content-addressing qui sert l'attestation sert *aussi* de clé de mémoïsation — pages et
  variantes non affectées ne sont jamais recalculées (§7).
- Build incrémental par graphe de dépendances ; pages en parallèle (pool de goroutines borné).
- Templates parsés mis en cache, invalidés par fsnotify ; modules wazero **compilés une fois**
  et réutilisés (cache de compilation wazero).
- Chemin chaud sobre en allocations : sérialisation en flux, buffers réutilisés (`sync.Pool`),
  zéro regex sur le HTML.
- Travail lourd (images) hors du chemin de sauvegarde : file asynchrone + progression SSE.
- **Outillage** : benchmarks Go + `benchstat` en CI sur les budgets ci-dessus ; `pprof` exposé
  en mode edit derrière un flag debug.
- **Performance du site publié** (l'autre moitié de l'invariant) : zéro JS par défaut
  (n'existent que les blocs opt-in type recherche), HTML/CSS minifiés, `srcset` responsive,
  assets adressés par contenu → cache navigateur immuable sans dépendre des en-têtes de l'hôte,
  CSS critique unique et léger (Pico + tokens).

---

## 16. Extensibilité — la frontière différée

- **V1** : le seul point d'extension est le *bloc nommé* — statique (donnée + CSS) ou computé
  built-in (`table`, `toc`).
- **V2** : blocs computés tiers via **WASM/wazero** — un module reçoit le sous-arbre du bloc +
  un contexte (dont les tables `data/`), rend du HTML du contrat ; sandbox par construction,
  agnostique au langage. Lightning CSS et libwebp passent déjà par cette même porte : la
  frontière existe avant d'être publique.
- Jamais de plugins au sens WordPress : pas de code accroché au cœur.

---

## 17. Stack récapitulative

| Couche | Choix | Justification courte |
|---|---|---|
| Langage cœur | **Go** (stdlib `net/http`, `embed`) | binaire unique, builds reproductibles, fiabilité décennale |
| Parsing HTML | `golang.org/x/net/html` | injection DOM fiable, jamais de regex |
| Sanitisation HTML | `bluemonday` | whitelist autoritaire (§4) |
| Sanitisation SVG | maison, sur `encoding/xml` | ~200 lignes de cœur de métier (§10.2) |
| Minification | `tdewolff/minify` | HTML + SVG, pur Go, déterministe |
| Versionnage | `go-git` | historique/undo natifs, pur Go |
| Watch/reload | `fsnotify` + SSE | live reload édition |
| CSS build | **esbuild** `pkg/api` (in-process) | minify + lowering, zéro Node |
| CSS langage | **natif** (custom properties, `@layer`, oklch) | tokens vivants au runtime |
| Tokens / thème défaut | **Open Props** / **Pico CSS v2** | gammes calibrées / style le HTML nu |
| Images raster | `disintegration/imaging` + **libwebp-WASM via wazero** (`gen2brain/webp`) | WebP sans CGo ; `-tags media_vips` en option native |
| Données | CSV/JSON plats ; `-tags duckdb` en option | vérité diffable ; `modernc.org/sqlite` si relationnel un jour |
| IA | client HTTP OpenAI-compatible (stdlib) | zéro modèle embarqué ; Ollama local ou API distante |
| Auth | `x/crypto/argon2` + sessions | V1 mono-admin |
| Éditeur | TypeScript vanilla, Shadow DOM, `embed.FS` | zéro framework, zéro contamination CSS |
| Extension (V2) | wazero (WASM) | sandbox ; porte déjà ouverte par libwebp et Lightning CSS |

---

## 18. Jalons

- **M0 — Le compilateur.** `build` : parsing, injection, blocs computés built-ins, passe
  esbuild, minification, assets hashés, symlink atomique, `site.lock`, **sorties SEO**
  (sitemap, canoniques, JSON-LD, OG). Tests d'or sur le déterminisme (deux builds = mêmes
  octets) ; **benchmarks et budgets posés dès ce jalon** (§15). *Déjà utile seul : un SSG
  minimaliste à thèmes échangeables.*
- **M1 — L'aller-retour.** Mode `edit` local : overlay, contenteditable, PUT/sanitisation,
  commit, build incrémental + mémoïsation, SSE. **80 % du risque vit ici** : robustesse
  DOM ↔ fragment. Corpus de tests d'or (whitespace, imbrications, collages
  Word/Docs/LibreOffice), fuzzing du round-trip sur le vocabulaire du contrat.
- **M2 — Les thèmes et la composition.** `theme.json` complet, validation de compatibilité,
  `theme apply`, panneau de tokens, **slots `stack`** (UX widgets) et panneaux de config des
  blocs, **panneau SEO + lint `--check`**. Thème défaut Pico+Open Props, second thème de
  démonstration (la preuve par deux).
- **M3 — Médias, données, distant.** Pipeline raster **WebP/wazero** + variante og-JPEG,
  **contrat SVG** + logo inline + favicons dérivés, file asynchrone ; **éditeur en grille
  `data/`** + blocs `table` ; auth, CSRF, publication (push/Pages).
- **M4 — Finitions produit.** **Assistant IA** (alt, descriptions, titres ; config
  fournisseur), historique/revert dans l'UI, packaging (releases multi-arch reproductibles),
  documentation du contrat de thème pour auteurs tiers.

**Risque n° 1, répété à dessein :** la fidélité de l'aller-retour DOM ↔ fragment. Tout le
reste est de l'ingénierie connue.

---

## 19. Décisions actées / décisions ouvertes

**Actées :** Go mono-binaire ; fragments et tables plates comme sources de vérité ; thèmes =
donnée (HTML + manifeste) ; contrats stricts (contenu §4, données §3.3, thème §5) ; CSS natif +
tokens, Open Props + Pico, esbuild in-process ; **WebP via libwebp-WASM/wazero, og:image en
JPEG, AVIF optionnel** ; **contrat SVG à deux profils, logo inline `currentColor`, favicons
dérivés** ; **slots `stack` = widgets sans nouvelle primitive, paramètres de blocs en `data-*`
whitelistés** ; **SEO : sorties au build + panneau meta + lint déterministe sans IA** ;
**IA = assistant d'édition uniquement, client OpenAI-compatible, zéro modèle embarqué, jamais
au build** ; go-git ; zéro BDD ; production 100 % statique ; performance = invariant à budgets
(§15) ; extension différée (blocs V1, WASM V2).

**Ouvertes :**

1. Le nom (« Palimpseste » à confirmer ou remplacer).
2. esbuild seul vs Lightning CSS-en-WASM dès V1.
3. Politique `builds/` dans git (artefact versionné vs branche `pages` orpheline).
4. Catalogue exact des blocs computés built-ins V1 (`table`, `toc` actés ; quoi d'autre ?).
5. Collections (blog) : périmètre précis de la V2 — et avec elles le bloc `recent` et JSON-LD
   `Article`.
6. UX de configuration du fournisseur IA (détection d'Ollama local ? presets ?).

---

## 20. Journal des révisions

- **0.2 — 2026-06-12.** Couche `data/` (contrat de données, grille d'édition, moteur en
  mémoire, patterns dérivés Pagefind/SQLite-artefact) ; pipeline média v2 (WebP par défaut via
  libwebp-WASM/wazero, ordre orientation→strip→encode, og:image JPEG, AVIF optionnel) ; contrat
  SVG (sanitiseur XML maison, profils `img`/`inline`, logo `currentColor`, favicons dérivés) ;
  type de slot `stack` (widgets) + paramètres de blocs `data-*` et blocs computés built-ins ;
  SEO (sorties au build, panneau meta, lint `--check`) ; assistant IA optionnel (édition
  seulement, OpenAI-compatible, Ollama local) ; nouvelle section **Performance — budgets et
  invariants** ; principes 3 (backend déplacé dans le temps), 8 et 9 ajoutés ; sécurité, stack,
  jalons et décisions mis à jour.
- **0.1 — 2026-06-12.** Version initiale.
