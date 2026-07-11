# Palimpseste

> *Un palimpseste est un parchemin gratté puis réécrit : des pages
> re-matérialisées indéfiniment au-dessus d'un contenu qui, lui, persiste.*

**En une phrase :** un CMS à édition in-place dans l'esprit de Sitecake, livré
comme un unique binaire Go, dont le contenu vit en fragments HTML sémantiques
et dont les pages publiées ne sont que des vues matérialisées —
`f(fragments, data, thème) → site statique`, déterministe et attestable.

La référence de conception est [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
(révision 0.2) ; chaque `§` cité dans le code et dans ce fichier y renvoie.

## Ce qui rend Palimpseste différent

- **La page est l'interface** (§1) : pas de back-office — on édite le site en
  le regardant, dans son rendu final.
- **Inversion du stockage** (§1) : le contenu est la vérité, stocké en
  fragments HTML portables sous `content/` ; `public/` est un artefact
  jetable, régénérable à l'identique.
- **Le backend est déplacé dans le temps** (§1) : un serveur complet existe —
  seulement pendant l'édition. En production : des fichiers statiques purs,
  surface d'attaque nulle.
- **Déterminisme de bout en bout** (§1, §7) : mêmes entrées + même binaire =
  artefact identique bit à bit, attesté par `site.lock` — et donc mémoïsable :
  le content-addressing qui sert la preuve sert aussi le cache.
- **Contrats stricts** (§4, §5) : le contenu est du HTML sémantique whitelisté
  côté serveur à chaque écriture ; un thème est de la donnée (templates HTML +
  CSS natif + manifeste), jamais du code.
- **La performance est un invariant** (§15) : les budgets sont des tests qui
  échouent en CI — pas des intentions.

## Démarrage rapide

```sh
# éditer le site d'exemple en place (ouvre un serveur local, loopback seul)
go run ./cmd/palimpseste edit -site examples/drake -open

# publier : builds/<hash>/ + bascule atomique du symlink public/ (§3, §7)
go run ./cmd/palimpseste build -site examples/drake

# contrôle qualité (lint SEO/structure, §11) — échoue en CI si régression
go run ./cmd/palimpseste build -site examples/drake -check -strict

# servir public/ pour vérifier (n'importe quel hébergeur statique fait pareil)
go run ./cmd/palimpseste serve -site examples/drake

# thèmes : lister, vérifier la compatibilité (§5.3), basculer
go run ./cmd/palimpseste theme list  -site examples/drake
go run ./cmd/palimpseste theme check -site examples/drake atelier
go run ./cmd/palimpseste theme apply -site examples/drake atelier

# édition distante authentifiée (§8, §14) et publication (§13)
go run ./cmd/palimpseste passwd                      # génère PALIMPSESTE_ADMIN_HASH
go run ./cmd/palimpseste edit -site examples/drake -listen
go run ./cmd/palimpseste publish -site examples/drake
```

L'assistant IA (§12) est optionnel et hors dépôt : exportez
`PALIMPSESTE_AI_ENDPOINT` + `PALIMPSESTE_AI_MODEL` (une API OpenAI-compatible ou
Ollama local). Non configuré, il n'existe pas. Il **propose** (alt, description,
titres) ; rien n'est écrit sans un geste humain, qui repasse par le contrat §4.

Dans l'éditeur : les régions en pointillé sont éditables, `Ctrl+S` enregistre
(sanitisation → écriture → commit git → build incrémental, §8), les boutons
**Thème**, **SEO** et **Vérifier** ouvrent les panneaux ; chaque sauvegarde est
un commit `edit(page/slot)` signé de votre identité git (§13).

## Les trois modes du binaire (§2.1)

| Mode | Commande | Rôle |
|---|---|---|
| **build** | `palimpseste build` | Matérialise `content/ + data/ + theme/ → public/`. One-shot, scriptable, CI-compatible. |
| **edit** | `palimpseste edit` | Serveur d'édition éphémère : pages matérialisées + overlay, API, loopback sans auth (distant : M3). |
| **serve** | `palimpseste serve` | Serveur statique de confort pour `public/`. Optionnel. |

## État des jalons (§18)

- ✅ **M0 — Le compilateur.** Matérialisation déterministe, SEO au build,
  assets adressés par contenu, `site.lock`, mémoïsation, symlink atomique.
- ✅ **M1 — L'aller-retour.** Mode `edit` local : overlay, PUT/sanitisation,
  commit-par-sauvegarde, SSE ; corpus d'or (Word/Docs/LibreOffice, whitespace,
  imbrications) + fuzzing du round-trip sur le vocabulaire du contrat.
- ✅ **M2 — Les thèmes et la composition.** `theme.json` complet, validation
  de compatibilité + `theme apply`, panneau de tokens (aperçu runtime,
  persistance de `tokens.css` seul), slots `stack` + panneaux de config
  générés du catalogue, panneau SEO + lint à la demande, la preuve par deux
  (`drake` ↔ `atelier`).
- ✅ **M3 — Médias, données, distant.** Contrat `data/` (CSV validé au schéma)
  + grille tableur + rendu du bloc `table` ; pipeline raster WebP/wazero
  (variantes responsive + `srcset`, og-JPEG, file asynchrone) ; contrat SVG
  (sanitiseur XML maison, logo inline `currentColor`, favicons dérivés) ;
  minification HTML + attestation étendue (`data/` + encodeur WASM) ; auth
  distante (argon2id, `edit --listen`) ; publication (`git-push`).
- ✅ **M4 — Finitions produit.** Assistant IA d'édition (§12) : client
  OpenAI-compatible, config hors dépôt, Ollama local, suggestions
  **advisory-only** (l'IA propose, jamais n'écrit) ; historique/revert dans
  l'UI (vue sur `git log`, restauration par le chemin normal) ; builds
  reproductibles multi-arch ; guide du contrat de thème pour auteurs tiers
  ([docs/themes.md](docs/themes.md)).

Le contrat d'architecture des §1–§17 est intégralement implémenté et vérifié.
Reste ouvert par choix du mainteneur : le thème par défaut sur framework CSS
(§6), fourni plus tard — le panneau de tokens est déjà agnostique.

Note : le thème par défaut sur framework CSS (§6) est volontairement différé —
le framework sera fourni par le mainteneur ; les thèmes de démonstration sont
en CSS natif pur et le panneau de tokens est agnostique au framework.

## La carte du code — un paquet par contrat

Le document d'architecture définit des **contrats** (contenu §4, blocs §4.1,
données §3.3, thème §5) autour d'un **moteur pur** (§7), entre deux
orchestrateurs temporels (§2.1). Le code suit exactement cette ontologie — si
un concept est dans le .md, il a un paquet, et un seul :

```
cmd/palimpseste          les modes du binaire : build, edit, serve, theme

internal/                     — couche 0 : primitive —
  render      helpers DOM sur x/net/html ; LA sérialisation canonique unique
              (tout ce qui écrit du HTML passe par lui : jamais de regex, §7)

                              — couche 1 : les contrats (données pures) —
  site        site.json : identité, routes, pages, méta SEO, publish (§3.1) ; Save atomique
  theme       theme.json complet §5.1 : slots typés, schémas data, tokens typés,
              templates ; lecture/écriture de tokens.css (§6)
  content     résolution slot → fragment sur disque, écriture atomique (§3.2)
  data        contrat data/ (§3.3) : CSV validé au schéma du thème (le bluemonday
              des données), écriture canonique atomique
  blocks      le catalogue §4.1 : blocs nommés, schémas de paramètres typés/bornés ;
              Schema() sérialisable pour les panneaux de config générés (§9)
  sanitize    LE gardien du contrat de contenu (§4) : whitelist bluemonday +
              normalisation de collage + validation structurelle (blocs, media/,
              alt, embed) ; FragmentForSlot applique le micro-contrat du slot
              (plain, stack, liste de blocs) ; toute écriture passe ici
  svg         le contrat SVG (§10.2) : sanitiseur XML maison (profils img/inline),
              logo inline currentColor, dérivation des favicons
  themecheck  la validation de compatibilité §5.3 : Check (rapport, jamais de
              mutation) et Apply (migrate renames + bascule site.json)
  auth        auth mono-admin distante (§14) : argon2id, sessions, rate-limit
  publish     l'acte de déploiement (§13) : git-push, credentials hors dépôt

                              — couche 2 : le moteur pur (§7) —
  materialize injection des fragments par data-slot + rendu des blocs computés
              (toc, table depuis data/) + logo SVG inline + srcset + résolution
              des URLs media
  media       pipeline raster (§10.1) : WebP via libwebp-WASM/wazero, variantes
              responsive, og-JPEG, file asynchrone — hors du chemin de sauvegarde
  css         passe esbuild in-process, bundle adressé par contenu
  seo         sitemap, canoniques, JSON-LD, OG — déterministes
  lint        le --check §11 : règles, zéro IA

                              — couche 3 : orchestration —
  build       le mode build : pages en parallèle, mémoïsation par
              content-addressing, site.lock, layout builds/<hash> + symlink
              public/ (§3, §7)

                              — couche 4 : le backend déplacé dans le temps —
  ai          l'assistant d'édition optionnel (§12) : client OpenAI-compatible
              (stdlib), config hors dépôt, advisory-only — propose, n'écrit jamais
  editserver  le mode edit : API §8 (CSRF + Origin + regex stricte §14),
              chaîne PUT = sanitisation → écriture → commit → build incrémental,
              SSE typé (build/error/reload/media), overlay TypeScript transpilé
              in-process (assets/app.ts, Shadow DOM intégral §9) ; API M2–M4 :
              theme, pages/{}/meta, check, data/{table}, media, publish,
              ai/suggest, history, revert ; auth distante en option (--listen)
  history     chaque sauvegarde = un commit go-git, auteur = utilisateur (§13) ;
              CommitPaths regroupe une migration en un commit ; Log/FileAt =
              la vue historique et le revert (§13)
```

**Règle de dépendance** : une couche n'importe que sous elle. `sanitize` et
`materialize` partagent `blocks` et `render` — c'est voulu : le sanitiseur et
le matérialiseur doivent lire le même catalogue et écrire les mêmes octets,
sinon l'aller-retour DOM ↔ fragment (le « risque n° 1 » du .md) dérive.

**Où intervenir** :
- changer ce qu'un fragment a le droit de contenir → `blocks` (catalogue) puis
  `sanitize` (application) puis le fuzzer (`sanitize/fuzz_test.go`) qui pinne
  le vocabulaire ;
- changer le rendu d'une page → `materialize` (jamais `build`, qui ne fait
  qu'orchestrer) ;
- ajouter un endpoint → `editserver` (routes + handler), en gardant l'ordre
  autorisation → validation regex → travail ;
- toucher à la sortie HTML → attendre un diff sur le test d'or
  (`internal/build`, régénérable avec `-update`) et bump éventuel de
  `cacheFormatVersion` (`build/cache.go`).

## Vérifier (rien d'autre n'est « fini »)

```sh
go build ./... && go vet ./... && go test ./...
go test ./internal/sanitize -run '^$' -fuzz FuzzFragmentRoundTrip -fuzztime 30s
go test ./internal/build -run TestBuildGolden -update   # si la sortie HTML change, sciemment
go test -bench . -count 10 ./internal/build ./internal/editserver | benchstat -  # dérive perf
```

Les budgets du §15 sont des tests (`TestBudget*` dans `build` et `editserver`) :
cycle de sauvegarde < 100 ms, matérialisation classe ~1 ms/page, RAM au repos
< 30 Mo. S'ils échouent, c'est un bug de performance, pas un test capricieux.

## Ergonomie cognitive (backend d'édition)

L'interface d'édition suit des principes explicites — documentés en tête de
[`editserver/assets/app.ts`](internal/editserver/assets/app.ts) et à préserver
dans toute évolution : divulgation progressive (rien n'apparaît qui ne
s'applique au contexte ; un seul panneau ouvert à la fois), reconnaissance
plutôt que rappel (libellés en toutes lettres, raccourcis affichés), un unique
point d'état en mots avec un code couleur constant (ambre = non sauvegardé,
vert = ok, rouge = erreur), prévention des pertes (gardes de navigation,
validation avant envoi). La règle de fond, héritée du §9 : **l'UI ne propose
jamais ce que le serveur refuserait.**

## Dogfooding

[`examples/drake/`](examples/drake/) est le site du trident
(DistroForge → Drake OS → Palimpseste) : site d'exemple, banc d'essai réel et
fixture du test d'or. Il embarque deux thèmes — `drake` et `atelier` — qui
partagent le même contrat de slots : la preuve par deux que re-thémer ne
touche ni contenu ni pages (§5).
