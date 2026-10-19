#!/usr/bin/env bash
# Comstac fast-lane deploys use the shared engine; see /home/james/src/fastdeploy.
#   deploy/deploy.sh [--quick | --rollback | --status | --prune [N]]
exec /home/james/src/fastdeploy/fastdeploy comstac "$@"
