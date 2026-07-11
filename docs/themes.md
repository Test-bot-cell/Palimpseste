# Écrire un thème Palimpseste

Un thème Palimpseste est **de la donnée, pas du code** (§5) : des templates HTML
purs, du CSS natif, et un manifeste `theme.json`. Il n'y a aucun langage de
template, aucune logique à écrire — l'injection du contenu se fait au niveau du
DOM, par le binaire. Ce guide suffit à écrire un thème complet sans lire le code.

Le thème d'exemple `examples/drake/themes/atelier/` est une référence vivante :
petit, en CSS natif pur, même contrat de slots que le thème `drake`.

## Arborescence

```
themes/<nom>/
├── theme.json            # le manifeste (obligatoire)
├── templates/
│   └── page.html         # un ou plusieurs templates HTML
└── styles/
    ├── tokens.css        # les tokens éditables en direct (optionnel)
    └── base.css          # le style du thème
```

Rien d'autre n'est imposé : nommez vos templates et vos feuilles comme vous
voulez, tant que `theme.json` les référence.

## `theme.json` — le manifeste

```json
{
  "name": "atelier",
  "version": "0.1.0",
  "slots": {
    "nav":    { "type": "nav" },
    "hero":   { "type": "richtext" },
    "main":   { "type": "richtext", "blocks": ["gallery", "columns", "cta", "toc", "table"] },
    "footer": { "type": "richtext" }
  },
  "styles": ["styles/tokens.css", "styles/base.css"],
  "tokens": {
    "--accent": { "type": "color" },
    "--rhythm": { "type": "length" }
  }
}
```

Champs :

- **`name`** (obligatoire) — l'identifiant du thème.
- **`version`** — libre, informative ; utile pour le suivi et l'attestation.
- **`slots`** — les régions éditables (voir plus bas). C'est le cœur du contrat.
- **`styles`** — la liste ordonnée des feuilles CSS, relatives au dossier du
  thème. Elles sont concaténées, minifiées et adressées par contenu au build.
- **`tokens`** — les variables CSS éditables en direct depuis l'éditeur (§6).
- **`templates`** — optionnel : une map `nom → chemin` si vous ne suivez pas la
  convention `templates/<nom>.html`.
- **`data`** — optionnel : les schémas de tables `data/` (§3.3).
- **`migrate`** — optionnel : renommages de slots par rapport au thème précédent
  (§5.3), appliqués comme un commit dédié lors d'un `theme apply`.

## Les types de slots

Chaque slot déclare un `type` qui décide de son micro-éditeur **et** du
micro-contrat que le serveur applique à ce qu'on y écrit. L'éditeur ne propose
jamais ce que le serveur refuserait.

| Type | Contenu autorisé | Éditeur |
|---|---|---|
| `plain` | texte nu, une ligne | champ inline |
| `richtext` | le vocabulaire sémantique complet (§4) | contenteditable + barre |
| `stack` | une pile de blocs, sans prose libre | liste réordonnable + config |
| `image` | une `figure`/`img` unique | sélecteur média |
| `nav` | une liste de liens | (édité comme du richtext en V1) |
| `data` | une table liée à `data/` | grille type tableur |

Options par slot :

- `blocks` — pour `richtext` et `stack` : la liste blanche des blocs nommés
  autorisés dans ce slot (un bloc hors liste se dégrade en HTML sémantique).
- `source` — pour `data` : le nom de la table `data/`.
- `inline: true` — pour `image` : incruste le SVG dans le HTML (le logo, §10.2).
- `formats` — pour `image` : `["raster"]`, `["vector"]` ou les deux.
- `maxLength` — pour `plain` : plafond indicatif de longueur.

## Les templates : HTML pur + `data-slot`

Un template est du HTML valide où chaque région éditable porte un attribut
`data-slot` dont la valeur est le nom du slot :

```html
<!doctype html>
<html>
  <head><meta charset="utf-8"><title>placeholder</title></head>
  <body>
    <header class="masthead">
      <nav data-slot="nav"></nav>
      <div class="hero" data-slot="hero"></div>
    </header>
    <main class="prose" data-slot="main"></main>
    <footer class="colophon" data-slot="footer"></footer>
  </body>
</html>
```

Au build, le binaire remplace le contenu de chaque `[data-slot]` par le fragment
correspondant, injecte le SEO dans le `<head>`, résout les URLs média, rend les
blocs computés, et retire les marqueurs `data-slot`. Le `<title>` est écrasé par
le SEO ; laissez un placeholder.

Le `<h1>` appartient au template, jamais au contenu (le contrat de contenu
interdit `h1` dans un fragment) — placez-le dans le template, autour d'un slot
`plain` si vous voulez qu'il soit éditable.

## Slots partagés (`_global`)

Un fragment peut être partagé entre pages via le préfixe `_global:` dans le
mapping `slots` d'une page (`site.json`), ou en nommant le slot `_global:<nom>`.
Le nav, le footer et le logo sont typiquement globaux.

## Le CSS : natif, en couches

Écrivez du CSS natif moderne — custom properties, nesting, `oklch()`,
`color-mix()`, `clamp()`, `@layer`. **Aucun préprocesseur** : les variables Sass
meurent à la compilation, les custom properties vivent au runtime — c'est ce qui
permet l'édition de thème en temps réel.

Convention utile : ordonnez vos couches, mettez les tokens éditables dans
`tokens.css` (une seule feuille, réécrite par l'éditeur) et le reste ailleurs.

```css
/* tokens.css — réécrit par le panneau de thème */
:root {
  --accent: oklch(0.55 0.16 30);
  --rhythm: 1.5rem;
}
```

```css
/* base.css */
@layer base, layout, blocks;

@layer base   { body { color: var(--ink); background: var(--paper); } }
@layer layout { .prose > * + * { margin-block-start: var(--rhythm); } }
@layer blocks { [data-block="cta"] { border-inline-start: 3px solid var(--accent); } }
```

### Les tokens éditables (§6)

Tout token déclaré dans `theme.json#tokens` **et** présent dans une feuille
nommée `tokens.css` devient éditable en direct depuis le panneau Thème de
l'éditeur : le contrôle est généré selon le `type` (`color`, `length`, `radius`,
`font`), l'aperçu est instantané, et « Appliquer » réécrit `tokens.css` — et
lui seul. Re-thémer ne touche ni le contenu ni les pages.

## Les blocs nommés (§4.1)

Les blocs sont des structures sémantiques conventionnelles portées par un
attribut `data-block`. Stylez-les par `[data-block="..."]`. Catalogue V1 :

| Bloc | Type | Paramètres | Élément(s) |
|---|---|---|---|
| `gallery` | statique | — | `figure`, `div`, `section` |
| `columns` | statique | `data-count` (2–4) | `div`, `section` |
| `cta` | statique | `data-variant` (`primary`/`subtle`) | `div`, `aside`, `section` |
| `embed` | statique | (iframe, domaines whitelistés) | `div`, `figure` |
| `table` | computé | `data-source` (nom de table) | `div` |
| `toc` | computé | `data-depth` (2–4) | `div`, `aside` |

Un bloc computé est rendu par le binaire au build (la `table` depuis `data/`, la
`toc` depuis les titres de la page). Un bloc que votre thème ne déclare pas dans
un slot reste du HTML sémantique valide — dégradation élégante, jamais de page
cassée.

## Les données (`data/`, §3.3)

Déclarez le schéma d'une table dans `theme.json` :

```json
"data": {
  "equipe": {
    "format": "csv",
    "columns": { "nom": "string", "role": "string", "photo": "media" }
  }
}
```

Types de colonnes : `string`, `number`, `bool`, `date` (ISO `AAAA-MM-JJ`),
`media` (chemin sous `media/`). Le serveur valide chaque écriture contre ce
schéma — c'est le bluemonday des données. Un bloc `table` avec
`data-source="equipe"` rend la table au build, cellules échappées.

## Compatibilité et bascule (§5.3)

Avant d'appliquer un thème, le binaire vérifie la compatibilité :

```sh
palimpseste theme check <nom>     # rapport, ne mute rien
palimpseste theme apply <nom>     # vérifie, migre, bascule, committe
```

Règles :

- slot requis absent alors que du contenu existe → **erreur bloquante** ;
- slot offert sans contenu → avertissement (fragment vide au rendu) ;
- bloc non déclaré → avertissement (dégradation §4.1) ;
- schéma `data` divergent → rapport de migration ;
- renommages → table `"migrate"` optionnelle, appliquée en commit dédié.

Pour qu'un thème soit un remplaçant compatible d'un autre, offrez **au moins**
les mêmes slots portant du contenu, avec des types compatibles. Le thème
`atelier` est compatible avec `drake` : même contrat de slots, mise en page
distincte — la preuve par deux.

## Le logo et les favicons (§10.2)

Déclarez un slot image inline pour le logo :

```json
"_global:logo": { "type": "image", "formats": ["vector"], "inline": true }
```

Le SVG pointé est assaini, incrusté dans le HTML avec `fill="currentColor"` (il
hérite donc de vos tokens et de `light-dark()`), et le binaire en dérive le jeu
de favicons (SVG natif + PNG de compatibilité + Apple touch), dont il injecte
les `<link>` dans chaque page.

## Check-list de l'auteur de thème

- [ ] `theme.json` : `name`, `slots` typés, `styles` ordonnés.
- [ ] Un template par valeur de `template` utilisée dans `site.json`.
- [ ] Chaque slot du manifeste a son `data-slot` dans un template.
- [ ] `tokens.css` séparé si vous voulez des tokens éditables.
- [ ] Blocs stylés par `[data-block="..."]` pour ceux que vos slots déclarent.
- [ ] `palimpseste theme check <nom>` ne renvoie aucune erreur bloquante.
- [ ] `palimpseste build -site <site> -check` passe le lint SEO/qualité.
