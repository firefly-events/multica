#!/bin/bash
export PATH=/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin
podman machine start 2>/dev/null || true
podman start multica-postgres-1 2>/dev/null || true
