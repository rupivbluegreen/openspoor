#!/usr/bin/env bash
# Coverage gate: fails when a domain package's statement coverage is below the minimum.
#
# Usage (from backend/):  ../scripts/coverage-gate.sh coverage.out [min-percent]
#
# coverage.out comes from `go test -coverpkg=./internal/...,./pkg/... -coverprofile=coverage.out ./...`.
# Coverage is computed per package (the directory of the source file) from the profile itself:
# statements covered by any test binary count as covered, so a package exercised only by another
# package's integration tests is credited.
#
# Domain packages are everything under internal/ and pkg/ except:
#   - cmd/ (process wiring) and the test support packages (internal/testkit, any *test directory),
#   - generated files (a "Code generated ... DO NOT EDIT." header, *.pb.go, *_gen.go, *.gen.go),
#   - packages without statements.
#
# Environment: COVERAGE_MIN (default 80), COVERAGE_EXCLUDE (extra egrep for package paths, optional),
# MODULE (module path; read from go.mod by default).
set -euo pipefail

profile=${1:-coverage.out}
min=${2:-${COVERAGE_MIN:-80}}
[ -f "$profile" ] || { echo "coverage-gate: $profile not found" >&2; exit 2; }
module=${MODULE:-$(awk '/^module /{print $2; exit}' go.mod)}
[ -n "$module" ] || { echo "coverage-gate: cannot determine the module path (run from backend/)" >&2; exit 2; }

# Generated files, as paths relative to the module root.
generated=$(mktemp)
trap 'rm -f "$generated"' EXIT
{
  grep -rlE '^// Code generated .* DO NOT EDIT\.$' --include='*.go' internal pkg 2>/dev/null || true
  find internal pkg -name '*.pb.go' -o -name '*_gen.go' -o -name '*.gen.go' 2>/dev/null || true
} | sort -u > "$generated"

awk -v module="$module" -v min="$min" -v extra="${COVERAGE_EXCLUDE:-}" -v genfile="$generated" '
BEGIN {
  while ((getline line < genfile) > 0) gen[line] = 1
  close(genfile)
}
NR == 1 && /^mode:/ { next }
{
  # <import path>/<file>.go:<start>,<end> <statements> <count>
  if (NF != 3) next
  block = $1; n = $2 + 0; c = $3 + 0
  stmts[block] = n
  if (c > 0) hit[block] = 1
}
END {
  for (block in stmts) {
    file = block; sub(/:[^:]*$/, "", file)
    if (index(file, module "/") != 1) continue
    rel = substr(file, length(module) + 2)
    if (rel !~ /^(internal|pkg)\//) continue
    if (rel in gen) continue
    dir = rel; sub(/\/[^\/]*$/, "", dir)
    if (dir ~ /(^|\/)(testkit|[a-z]*test|testdata)(\/|$)/) continue
    if (extra != "" && dir ~ extra) continue
    total[dir] += stmts[block]
    if (block in hit) covered[dir] += stmts[block]
  }
  fail = 0; checked = 0; gt = 0; gc = 0
  for (dir in total) {
    if (total[dir] == 0) continue
    pct = 100 * covered[dir] / total[dir]
    status = (pct + 1e-9 < min) ? "FAIL" : "ok"
    if (status == "FAIL") fail++
    checked++; gt += total[dir]; gc += covered[dir]
    printf "%-4s %6.1f%%  %5d/%-5d  %s\n", status, pct, covered[dir], total[dir], dir | "sort -k2 -n"
  }
  close("sort -k2 -n")
  if (checked == 0) { print "coverage-gate: no domain package found in the profile" > "/dev/stderr"; exit 2 }
  printf "\ndomain packages: %d, statements: %d/%d (%.1f%%), minimum per package: %s%%\n", checked, gc, gt, 100 * gc / gt, min
  if (fail > 0) { printf "coverage-gate: %d package(s) below %s%%\n", fail, min > "/dev/stderr"; exit 1 }
}' "$profile"
