# Bundled modified core

`sing-box/` is a build-source snapshot of wyx2685/sing-box_mod based on
8d054dcd8bfe (v1.13.0-alpha.5.0.20251202212447), with the reviewed AnyTLS PROXY and
audit patches through local core commit fbc8c50. It replaces only the previous
sing-box module dependency. Original authorship and LICENSE are retained.

Changes add optional pre-TLS PROXY parsing, authenticated panel-ID audit events,
normal-close filtering, policy/UoT failure coverage and stream-shared dedup.
See docs/ANYTLS.md and corresponding tests. The root V2bX remains MPL-2.0;
this component retains its own GPL-3.0-or-later terms and naming restriction.
Release archives carry both licenses. Source is included in this repository,
not downloaded from an unpublished developer path. Other dependencies remain
pinned to the previous release versions.

Unrelated upstream documentation, release packaging, CI and standalone test
fixtures are omitted. Runtime packages, module files, license and the AnyTLS/
PROXY regression tests are retained; no deployed credentials are included.
