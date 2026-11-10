#!/usr/bin/env bash
# Separate non-race observations. Run without concurrent benchmark processes.
set -euo pipefail
go test -count=1 -tags=integration,p7capacity -p 1 -run '^TestP7CapacityHistories$' -v -timeout=30m ./integration
go test -count=1 -tags=integration -p 1 -run '^(TestAnalysisPerformanceAndCapacity|TestTrainingPerformanceAndCapacity)$' -v -timeout=30m ./integration
go test -count=1 -tags=integration,scale -p 1 -run '^TestScale' -v -timeout=30m ./integration
if [[ -n "${RAI_TEST_PG_CONTAINER:-}" ]] && command -v docker >/dev/null;then
 docker stats --no-stream --format 'P7_CAPACITY_PG_RESOURCE={{json .}}' "$RAI_TEST_PG_CONTAINER"
 echo "P7_CAPACITY_PG_PEAK=$(docker exec "$RAI_TEST_PG_CONTAINER" cat /sys/fs/cgroup/memory.peak)"
fi
echo 'P7_CAPACITY_GATES=PASS'
