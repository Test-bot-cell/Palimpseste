#!/usr/bin/env bash
# release.sh — builds reproductibles multi-arch de palimpseste.
#
# Déterminisme (§6, §15) : mêmes entrées + même toolchain = mêmes octets. On
# neutralise toutes les sources de non-déterminisme du linker Go :
#   -trimpath        efface les chemins absolus de compilation ;
#   -buildid=        vide le build-id (sinon dérivé du hash des inputs, instable
#                    entre environnements) ;
#   CGO_ENABLED=0    binaire statique pur Go (le pipeline média est déjà
#                    WASM/wazero, aucun CGo requis) ;
#   VERSION injectée en -X, pas lue du système.
#
# Vérification : chaque cible est buildée DEUX fois et les deux binaires doivent
# être identiques bit à bit, sinon le script échoue. Les sommes SHA256 sont
# écrites dans dist/SHA256SUMS.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="dist/release"
LDFLAGS="-s -w -buildid= -X main.version=${VERSION}"

# Cibles : les plateformes de bureau et serveur courantes. Ajoutez-en ici.
TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
)

rm -rf "$OUT"
mkdir -p "$OUT"

echo "palimpseste ${VERSION} — build reproductible multi-arch"
echo

for target in "${TARGETS[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  ext=""
  [ "$goos" = "windows" ] && ext=".exe"
  name="palimpseste_${VERSION}_${goos}_${goarch}${ext}"
  out="$OUT/$name"

  build() {
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$1" ./cmd/palimpseste
  }

  # Build twice, into different paths, and require identical bytes.
  build "$out"
  tmp="$(mktemp)"
  build "$tmp"
  if ! cmp -s "$out" "$tmp"; then
    echo "NON REPRODUCTIBLE: $target diffère entre deux builds" >&2
    rm -f "$tmp"
    exit 1
  fi
  rm -f "$tmp"

  printf "  %-32s %s\n" "$name" "$(du -h "$out" | cut -f1)"
done

echo
( cd "$OUT" && sha256sum ./* > SHA256SUMS )
echo "sommes SHA256 → $OUT/SHA256SUMS"
echo
echo "Reproductibilité vérifiée : chaque cible buildée deux fois = mêmes octets."
