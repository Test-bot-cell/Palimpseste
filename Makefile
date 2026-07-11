# Palimpseste — cibles de développement et de release.
# La vérité de build reste `go build ./...` ; ce Makefile n'est qu'un raccourci.

.PHONY: build test vet fmt fuzz bench release check

build:
	go build ./...

test:
	go build ./... && go vet ./... && go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Round-trip fuzzers : contrat de contenu + sanitiseur SVG (§4, §10.2).
fuzz:
	go test ./internal/sanitize -run '^$$' -fuzz FuzzFragmentRoundTrip -fuzztime 30s
	go test ./internal/svg      -run '^$$' -fuzz FuzzSanitize          -fuzztime 30s

# Budgets de performance (§15) tenus comme tests.
bench:
	go test -run TestBudget ./internal/build ./internal/editserver
	go test -bench . -count 6 ./internal/build ./internal/editserver

# Builds reproductibles multi-arch (§6, §15).
release:
	./scripts/release.sh

# Régénère le test d'or après un changement de sortie HTML assumé.
golden:
	go test ./internal/build -run TestBuildGolden -update
