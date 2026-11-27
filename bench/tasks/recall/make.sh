#!/bin/bash
# make.sh [--solved] DIR: builds the ledgercheck repo in DIR for the `recall`
# benchmark task (Issue #7, item 3: retention of facts discovered in earlier
# tool results after automatic context trimming or summarization).
#
# Six service packages (alpha..zeta) each come with a ~24 KB operational log
# (~144 KB total, ~48K+ tokens when read via tools) containing one buried
# ANOMALY_CODE and one operational limit. None of those facts appear in
# prompt.txt. After fixing all six services, the final `summary` package must
# synthesize all six discovered codes and limits into one verified report.
set -euo pipefail
solved=0
if [ "${1:-}" = --solved ]; then solved=1; shift; fi
dir=${1:?usage: make.sh [--solved] DIR}
[ ! -e "$dir" ] || { echo "make.sh: $dir exists" >&2; exit 2; }
mkdir -p "$dir"
cd "$dir"
mkdir -p logs alpha beta gamma delta epsilon zeta summary

cat > go.mod <<'EOF'
module example.com/ledgercheck

go 1.21
EOF

cat > README.md <<'EOF'
# ledgercheck

Six node services (`alpha`, `beta`, `gamma`, `delta`, `epsilon`, `zeta`) and a
cross-service `summary` package. Each service has an operational trace in
`logs/<service>.log` recording one incident marker (`ANOMALY_CODE=...`) and one
calibrated limit (`LIMIT_VALUE=...`). Run tests with `go test ./...`.
EOF

# Deterministic service definitions: name|code|limit|metric|needle_line
SERVICES=(
  "alpha|ERR-ALP-419|480|BatchCeiling|118"
  "beta|ERR-BET-732|175|RetryJitterMs|164"
  "gamma|ERR-GAM-208|67|QuorumPct|91"
  "delta|ERR-DEL-584|900|EpochWindowSec|143"
  "epsilon|ERR-EPS-915|32|MaxInFlight|129"
  "zeta|ERR-ZET-641|128|ShardFanout|177"
)

for spec in "${SERVICES[@]}"; do
  IFS='|' read -r pkg code limit metric needle <<< "$spec"

  # Generate a deterministic ~24 KB structured operational trace log with one
  # buried incident line at line $needle.
  {
    for i in $(seq 1 220); do
      ts=$(printf "2026-11-20T08:%02d:%02dZ" $((i / 60)) $((i % 60)))
      if [ "$i" -eq "$needle" ]; then
        printf "%s level=ERROR service=%s seq=%04d event=audit_anomaly ANOMALY_CODE=%s LIMIT_KEY=%s LIMIT_VALUE=%s detail=\"calibrated threshold exceeded during replay checkpoint\"\n" \
          "$ts" "$pkg" "$i" "$code" "$metric" "$limit"
      else
        printf "%s level=INFO service=%s seq=%04d event=heartbeat peers=7 mem_kb=%d queue_depth=%d epoch=%d digest=%08x%08x\n" \
          "$ts" "$pkg" "$i" $((12400 + i * 17)) $((i % 9)) $((100 + i / 10)) $((i * 2654435761 % 4294967296)) $(( (i + 31) * 1597334677 % 4294967296))
      fi
    done
  } > "logs/${pkg}.log"

  if [ "$solved" -eq 1 ]; then
    cat > "${pkg}/service.go" <<EOF
package ${pkg}

// Report returns the calibrated service name, anomaly code, and operational limit
// recorded in logs/${pkg}.log.
type Report struct {
	Service string
	Code    string
	Metric  string
	Limit   int
}

func Current() Report {
	return Report{
		Service: "${pkg}",
		Code:    "${code}",
		Metric:  "${metric}",
		Limit:   ${limit},
	}
}
EOF
  else
    cat > "${pkg}/service.go" <<EOF
package ${pkg}

// Report returns the calibrated service name, anomaly code, and operational limit
// recorded in logs/${pkg}.log.
type Report struct {
	Service string
	Code    string
	Metric  string
	Limit   int
}

// Current must return the exact Service, Code (ANOMALY_CODE), Metric (LIMIT_KEY),
// and Limit (LIMIT_VALUE) recorded in logs/${pkg}.log.
func Current() Report {
	return Report{
		Service: "${pkg}",
		Code:    "TODO",
		Metric:  "${metric}",
		Limit:   0,
	}
}
EOF
  fi

  cat > "${pkg}/service_test.go" <<EOF
package ${pkg}

import "testing"

func TestCurrent(t *testing.T) {
	got := Current()
	if got.Service != "${pkg}" {
		t.Fatalf("Service = %q, want %q", got.Service, "${pkg}")
	}
	if got.Code != "${code}" {
		t.Fatalf("Code = %q, want %q", got.Code, "${code}")
	}
	if got.Metric != "${metric}" {
		t.Fatalf("Metric = %q, want %q", got.Metric, "${metric}")
	}
	if got.Limit != ${limit} {
		t.Fatalf("Limit = %d, want %d", got.Limit, ${limit})
	}
}
EOF
done

if [ "$solved" -eq 1 ]; then
  cat > summary/report.go <<'EOF'
package summary

import (
	"fmt"
	"strings"
)

// Entry is one service's audited anomaly code and calibrated operational limit.
type Entry struct {
	Service string
	Code    string
	Limit   int
}

// Entries returns the six audited services in alphabetical order using the exact
// ANOMALY_CODE and LIMIT_VALUE discovered from logs/<service>.log without
// importing the service packages.
func Entries() []Entry {
	return []Entry{
		{Service: "alpha", Code: "ERR-ALP-419", Limit: 480},
		{Service: "beta", Code: "ERR-BET-732", Limit: 175},
		{Service: "gamma", Code: "ERR-GAM-208", Limit: 67},
		{Service: "delta", Code: "ERR-DEL-584", Limit: 900},
		{Service: "epsilon", Code: "ERR-EPS-915", Limit: 32},
		{Service: "zeta", Code: "ERR-ZET-641", Limit: 128},
	}
}

// AuditDigest formats all six entries as "service=CODE:LIMIT" joined by ";"
// followed by "|total=<sum>".
func AuditDigest() string {
	entries := Entries()
	parts := make([]string, 0, len(entries))
	total := 0
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf("%s=%s:%d", e.Service, e.Code, e.Limit))
		total += e.Limit
	}
	return fmt.Sprintf("%s|total=%d", strings.Join(parts, ";"), total)
}
EOF
else
  cat > summary/report.go <<'EOF'
package summary

import (
	"fmt"
	"strings"
)

// Entry is one service's audited anomaly code and calibrated operational limit.
type Entry struct {
	Service string
	Code    string
	Limit   int
}

// Entries must return the six audited services (alpha, beta, gamma, delta,
// epsilon, zeta) in that order using the exact ANOMALY_CODE and LIMIT_VALUE
// discovered from logs/<service>.log. Keep this package standalone (do not
// import the sibling service packages).
func Entries() []Entry {
	return nil
}

// AuditDigest formats all six entries as "service=CODE:LIMIT" joined by ";"
// followed by "|total=<sum>".
func AuditDigest() string {
	entries := Entries()
	parts := make([]string, 0, len(entries))
	total := 0
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf("%s=%s:%d", e.Service, e.Code, e.Limit))
		total += e.Limit
	}
	return fmt.Sprintf("%s|total=%d", strings.Join(parts, ";"), total)
}
EOF
fi

cat > summary/report_test.go <<'EOF'
package summary

import "testing"

func TestAuditDigest(t *testing.T) {
	want := "alpha=ERR-ALP-419:480;beta=ERR-BET-732:175;gamma=ERR-GAM-208:67;delta=ERR-DEL-584:900;epsilon=ERR-EPS-915:32;zeta=ERR-ZET-641:128|total=1782"
	if got := AuditDigest(); got != want {
		t.Fatalf("AuditDigest() =\n  %q\nwant:\n  %q", got, want)
	}
}
EOF

git init -q
git config user.name bench
git config user.email bench@example.com
git add -A
git commit -q -m "initial"
