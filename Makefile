# Thin front door for the maintained scripts. Every target delegates: the
# scripts own the flags, the sharding and the environment, so a Makefile target
# cannot drift from CI or from a developer's terminal.
.PHONY: help build build-linux build-windows test fast pii ui race site site-check release clean

help:
	@printf '%s\n' \
	  'make build        build Linux and Windows binaries into dist/' \
	  'make test         exhaustive release gate (scripts/test.sh)' \
	  'make fast         fast inner gate (scripts/test-fast.sh)' \
	  'make pii          scan tracked files for personal data and environment residue' \
	  'make ui           Playwright suite (scripts/test-ui.sh)' \
	  'make race         whole tree under -race (scripts/test-race.sh)' \
	  'make site         regenerate the public pages and brand assets' \
	  'make site-check   fail when a generated public file is stale' \
	  'make release      build + installers, needs VERSION'

build:
	scripts/build.sh all

build-linux:
	scripts/build.sh linux

build-windows:
	scripts/build.sh windows

fast:
	scripts/test-fast.sh

pii:
	scripts/check-pii.sh

test:
	scripts/test.sh

ui:
	scripts/test-ui.sh

race:
	scripts/test-race.sh

site:
	go run ./cmd/gensite -write

site-check:
	go run ./cmd/gensite -check

release: build
	@command -v iscc >/dev/null || { echo 'release: iscc (Inno Setup) is not installed' >&2; exit 1; }
	VERSION="$${VERSION:-0.0.0-dev}" scripts/build.sh windows
	iscc "/DAppVersion=$${VERSION:-0.0.0-dev}" "/DSourceExe=$(CURDIR)/dist/mailsense.exe" packaging/windows/mailsense.iss

clean:
	rm -rf dist
