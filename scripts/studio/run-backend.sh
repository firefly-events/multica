#!/bin/bash
export PATH=/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin
set -a; source /Users/hive/Code/spikes/multica/.env; set +a
export PORT=8080
cd /Users/hive/Code/spikes/multica
# wait for postgres before booting (migrations run at startup)
for i in $(seq 1 30); do
  podman exec multica-postgres-1 pg_isready -U multica >/dev/null 2>&1 && break
  sleep 2
done
exec /Users/hive/Code/spikes/multica/server/bin/server
