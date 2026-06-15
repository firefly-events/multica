#!/bin/bash
export PATH=/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin
set -a; source /Users/hive/Code/spikes/multica/.env; set +a
export PORT=3000
cd /Users/hive/Code/spikes/multica
exec pnpm --filter @multica/web start
