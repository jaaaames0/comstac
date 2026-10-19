#!/usr/bin/env bash
# Fast-lane Comstac deploy for code-only changes (see AGENTS.md, handbook §9.1).
#
#   deploy/deploy.sh            verify, build, install a new immutable release,
#                               switch the `current` symlink and restart
#   deploy/deploy.sh --quick    as above, but `go test ./...` instead of `make verify`
#   deploy/deploy.sh --rollback switch back to the `previous` release and restart
#   deploy/deploy.sh --status   show current/previous/running release
#   deploy/deploy.sh --prune [N] delete releases other than current, previous and
#                               the newest N (default 5), after confirmation
#   deploy/deploy.sh --setup    one-time: create the symlinks, point the unit and
#                               /usr/local/bin/comstac at `current` (no restart)
#
# Releases with new migrations get an encrypted backup (comstac-backup.service)
# before the switch. Config, credential, unit-sandbox, listener and destructive
# schema changes are not fast-lane changes; use the full procedure for those.
set -euo pipefail

APP=comstac
UNIT=comstac.service
LIB=/usr/local/lib/comstac
ENV_FILE=/etc/comstac/comstac.env
BIN_LINK=/usr/local/bin/comstac
DROPIN=/etc/systemd/system/comstac.service.d/20-release.conf
MIGRATIONS=internal/store/migrations
BACKUP_UNIT=comstac-backup.service

die() { echo "deploy: $*" >&2; exit 1; }
log() { echo "==> $*"; }

SELF=$(realpath "$0")
[[ $EUID -ne 0 ]] || die "run as james, not root; sudo is used where needed"
cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

# Release name -> 12-char commit (prod-YYYYMMDD.N-g<sha12>), empty if none.
release_sha() { [[ $1 =~ -g([0-9a-f]{12})$ ]] && echo "${BASH_REMATCH[1]}" || true; }
link_target() { [[ -L $LIB/$1 ]] && readlink "$LIB/$1" || true; }
running_exec() { systemctl show -P ExecStart "$UNIT" | sed -n 's/.*path=\([^ ;]*\).*/\1/p'; }

# Atomically point $LIB/<name> at a release directory name.
set_link() {
  sudo ln -sfn "$2" "$LIB/.$1.new"
  sudo mv -T "$LIB/.$1.new" "$LIB/$1"
}

migrations_changed() { # old_sha new_sha; true if unknown or different
  [[ -n $1 ]] && git cat-file -e "$1^{commit}" 2>/dev/null || return 0
  ! git diff --quiet "$1" "$2" -- "$MIGRATIONS"
}

restart_and_check() {
  local pid
  sudo systemctl restart "$UNIT"
  sleep 1; pid=$(systemctl show -P MainPID "$UNIT")
  sleep 5 # outlast RestartSec so a crash loop shows as a changed PID
  if systemctl is-active --quiet "$UNIT" && [[ $pid != 0 && $(systemctl show -P MainPID "$UNIT") == "$pid" ]]; then
    systemctl show -p MainPID -p NRestarts -p ActiveEnterTimestamp "$UNIT"
    sudo journalctl -u "$UNIT" -n 15 --no-pager -o cat
    return 0
  fi
  sudo journalctl -u "$UNIT" -n 40 --no-pager -o cat
  return 1
}

cmd_status() {
  echo "current:  $(link_target current)"
  echo "previous: $(link_target previous)"
  echo "unit ExecStart: $(running_exec)"
  systemctl show -p ActiveState -p MainPID -p NRestarts "$UNIT"
}

cmd_setup() {
  [[ -L $LIB/current ]] && die "already set up: $(link_target current)"
  local exec rel prev
  exec=$(running_exec)
  [[ $exec == "$LIB"/*/comstac ]] || die "unexpected ExecStart $exec"
  rel=$(basename "$(dirname "$exec")")
  prev=$(cd "$LIB" && ls -1d prod-* | grep -vx "$rel" | sort -V | tail -1)
  log "current=$rel previous=$prev"
  cmp -s "$exec" "$BIN_LINK" || die "$BIN_LINK differs from the running binary; inspect before replacing it"
  sudo -v
  set_link current "$rel"
  set_link previous "$prev"
  printf '[Service]\nExecStart=\nExecStart=%s/current/comstac\n' "$LIB" |
    sudo install -o root -g root -m 0644 /dev/stdin "$DROPIN"
  sudo ln -sfn "$LIB/current/comstac" "$BIN_LINK.new"
  sudo mv -T "$BIN_LINK.new" "$BIN_LINK"
  sudo systemctl daemon-reload
  # The running process already executes these bytes; the new path applies at next start.
  cmp -s "$exec" "$LIB/current/comstac" || die "current symlink does not match running binary"
  cmd_status
}

cmd_deploy() {
  local verify=${1:-full}
  [[ -L $LIB/current ]] || die "run --setup first"
  [[ -z $(git status --porcelain) ]] || die "working tree is dirty; commit first"

  local sha short cur cur_sha
  sha=$(git rev-parse HEAD); short=${sha:0:12}
  cur=$(link_target current); cur_sha=$(release_sha "$cur")
  [[ $cur_sha != "$short" ]] || die "$short is already current ($cur)"

  if [[ $verify == quick ]]; then log "go test ./..."; go test ./...
  else log "make verify"; make verify; fi

  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  log "build $short"
  go build -trimpath -buildvcs=true -o "$tmp/comstac" ./cmd/comstac

  local today n ver
  today=$(date -u +%Y%m%d); n=1
  while ver="prod-$today.$n-g$short"; [[ -e $LIB/$ver ]] || git rev-parse -q --verify "refs/tags/production/$ver" >/dev/null; do
    n=$((n + 1))
  done

  sudo -v
  log "install $LIB/$ver"
  sudo install -d -o root -g root -m 0755 "$LIB/$ver"
  sudo install -o root -g root -m 0555 "$tmp/comstac" "$LIB/$ver/comstac"
  sudo chmod 0555 "$LIB/$ver"

  log "check-config against $ENV_FILE as $APP"
  if ! sudo systemd-run --quiet --pipe --wait --collect \
      -p User="$APP" -p Group="$APP" -p EnvironmentFile="$ENV_FILE" \
      -p WorkingDirectory=/var/lib/comstac -p ProtectSystem=strict -p ProtectHome=yes \
      -p NoNewPrivileges=yes "$LIB/$ver/comstac" check-config; then
    sudo rm -rf "${LIB:?}/$ver"
    die "check-config failed; release removed, nothing switched"
  fi

  if migrations_changed "$cur_sha" "$sha"; then
    log "migrations changed since $cur: running $BACKUP_UNIT"
    sudo systemctl start "$BACKUP_UNIT" || die "backup failed; nothing switched"
  fi

  log "switch $cur -> $ver"
  set_link previous "$cur"
  set_link current "$ver"
  if restart_and_check; then
    git tag "production/$ver"
    log "deployed $ver (sha256 $(sha256sum "$LIB/$ver/comstac" | cut -d' ' -f1)); rollback: deploy/deploy.sh --rollback"
  else
    echo "deploy: $UNIT failed to start; rolling back to $cur" >&2
    set_link current "$cur"
    set_link previous "$ver"
    restart_and_check || die "rollback also failed; investigate now"
    die "rolled back to $cur"
  fi
}

cmd_rollback() {
  local cur prev
  cur=$(link_target current); prev=$(link_target previous)
  [[ -n $prev && -d $LIB/$prev ]] || die "no previous release"
  if migrations_changed "$(release_sha "$prev")" "$(release_sha "$cur")"; then
    echo "WARNING: $cur added migrations after $prev; the older binary must tolerate the newer schema." >&2
    read -rp "Continue? [y/N] " a; [[ $a == y ]] || exit 1
  fi
  sudo -v
  log "switch $cur -> $prev"
  set_link current "$prev"
  set_link previous "$cur"
  restart_and_check || die "$UNIT not active after rollback; investigate now"
}

cmd_prune() {
  local keep=${1:-5} cur prev
  cur=$(link_target current); prev=$(link_target previous)
  # Oldest first by install time, so the newest $keep survive.
  mapfile -t victims < <(cd "$LIB" && ls -1dtr prod-* trial-* 2>/dev/null |
    grep -vx -e "$cur" -e "$prev" | head -n -"$keep")
  ((${#victims[@]})) || { echo "nothing to prune"; return; }
  printf '  %s\n' "${victims[@]}"
  read -rp "Delete these ${#victims[@]} releases? (rebuildable from git tags) [y/N] " a
  [[ $a == y ]] || exit 1
  for v in "${victims[@]}"; do sudo rm -rf "${LIB:?}/$v"; done
}

case ${1:-} in
  "") cmd_deploy full ;;
  --quick) cmd_deploy quick ;;
  --rollback) cmd_rollback ;;
  --status) cmd_status ;;
  --prune) cmd_prune "${2:-5}" ;;
  --setup) cmd_setup ;;
  *) sed -n '2,17p' "$SELF"; exit 2 ;;
esac
