#!/bin/sh
# Deploy the CrossStream server image to a Linux host over SSH, check it with a
# handshake probe, keep the previous image for rollback (phase 17).
#
#   remote-deploy.sh deploy <version> [xs-server-image-<version>-amd64.tar.gz]
#   remote-deploy.sh rollback          back to the previous version (health-checked)
#   remote-deploy.sh status            deployed versions, container state, xs-ctl info
#   remote-deploy.sh probe             health check only
#
# Environment (local side):
#   XS_DEPLOY_HOST  user@host (required); the user must be in the docker group
#   XS_DEPLOY_DIR   directory on the host, relative to the user's home (default crossstream)
#   XS_DEPLOY_MODE  host (default, docker-compose.host.yml) or bridge
#   XS_DEPLOY_SSH   ssh command with options (default: ssh -o BatchMode=yes)
#
# deploy copies the compose files and this script to the host, loads the image
# tarball (docker load; images never come from a registry), starts the version
# with docker compose and runs the health check: xs-ctl info reports that
# version, and xs-hubctl probe (inside the container, identity "healthcheck"
# in /data/healthcheck, enrolled on first use) completes a handshake on
# 127.0.0.1. A failed check switches back to the version that ran before and
# exits 1. .env on the host keeps XS_IMAGE_TAG and XS_PREVIOUS_IMAGE_TAG; older
# images are removed. The same script runs on the host with --on-server.
set -eu

IMAGE=crossstream/xs-server

die() {
	echo "remote-deploy: $*" >&2
	exit 1
}

# ---- server side: runs in the deploy directory on the host ----

envget() { sed -n "s/^$1=//p" .env 2>/dev/null | tail -n 1; }

envset() {
	touch .env
	if grep -q "^$1=" .env; then
		sed -i "s|^$1=.*|$1=$2|" .env
	else
		echo "$1=$2" >>.env
	fi
}

dc() {
	if [ "$(envget XS_DEPLOY_MODE)" = bridge ]; then
		docker compose -f docker-compose.yml "$@"
	else
		docker compose -f docker-compose.yml -f docker-compose.host.yml "$@"
	fi
}

# health <version>: the running server is <version> and answers a handshake.
health() {
	port=$(envget XS_DEPLOY_PROBE_PORT)
	port=${port:-51900}
	i=0
	until info=$(dc exec -T server xs-ctl info 2>&1); do
		i=$((i + 1))
		[ "$i" -lt 30 ] || {
			echo "health: no control socket after 30 s: $info"
			dc logs --no-color --tail 20 server
			return 1
		}
		sleep 1
	done
	ver=$(echo "$info" | awk '$1 == "version" { print $2 }')
	[ "$ver" = "$1" ] || {
		echo "health: server reports version '$ver', want '$1'"
		return 1
	}
	hc="xs-hubctl -dir /data/healthcheck probe -to 127.0.0.1:$port"
	listed=no
	dc exec -T server xs-ctl list 2>/dev/null | grep -q '^healthcheck ' && listed=yes
	if dc exec -T server test -f /data/healthcheck/server.json; then
		dc exec -T server $hc && return 0
		# Identity revoked or lost on the server (state restored, revoke in the
		# panel): enroll it again once; a server that does not answer fails again.
		[ "$listed" = yes ] && return 1
		echo "health: probe identity not enrolled any more, enrolling it again"
	fi
	dc exec -T server rm -f /data/healthcheck/server.json
	[ "$listed" = no ] || dc exec -T server xs-ctl revoke healthcheck >/dev/null
	code=$(dc exec -T server xs-ctl enroll healthcheck 2>/dev/null | grep '^XS1\.') || {
		echo "health: xs-ctl enroll healthcheck failed"
		return 1
	}
	dc exec -T server $hc -code "$code"
}

# start <version>: run that image with the compose files (no build, no pull).
start() {
	docker image inspect "$IMAGE:$1" >/dev/null 2>&1 || {
		echo "image $IMAGE:$1 is not on this host (load it: deploy <version> <tarball>)"
		return 1
	}
	XS_IMAGE_TAG=$1 dc up -d --no-build --pull never
}

# keep only the current and the previous image
prune() {
	docker image ls "$IMAGE" --format '{{.Tag}}' | while read -r t; do
		case "$t" in "$1" | "$2" | "<none>") ;; *) docker image rm "$IMAGE:$t" >/dev/null 2>&1 || true ;; esac
	done
}

# switch <version>: start it and check it; on failure go back to the current one.
switch() {
	new=$1
	cur=$(envget XS_IMAGE_TAG)
	prev=$(envget XS_PREVIOUS_IMAGE_TAG)
	echo "switching $IMAGE: ${cur:-none} -> $new"
	if start "$new" && health "$new"; then
		if [ -n "$cur" ] && [ "$cur" != "$new" ]; then
			prev=$cur
			envset XS_PREVIOUS_IMAGE_TAG "$prev"
		fi
		envset XS_IMAGE_TAG "$new"
		prune "$new" "$prev"
		echo "OK: running $new (rollback target: ${prev:-none})"
		return 0
	fi
	echo "FAILED: $new did not pass the health check"
	if [ -n "$cur" ] && [ "$cur" != "$new" ]; then
		echo "rolling back to $cur"
		if start "$cur" && health "$cur"; then
			echo "rolled back: running $cur"
		else
			echo "ROLLBACK FAILED: check 'docker compose logs server' on the host"
		fi
	fi
	return 1
}

on_server() {
	cmd=${1:-}
	[ -f docker-compose.yml ] || die "no docker-compose.yml in $(pwd)"
	case "$cmd" in
	deploy)
		[ -n "${2:-}" ] || die "deploy: version missing"
		[ -n "${XS_DEPLOY_MODE:-}" ] && envset XS_DEPLOY_MODE "$XS_DEPLOY_MODE"
		mkdir -p data
		switch "$2"
		;;
	rollback)
		prev=$(envget XS_PREVIOUS_IMAGE_TAG)
		[ -n "$prev" ] || die "rollback: no previous version recorded in .env"
		switch "$prev"
		;;
	probe)
		health "$(envget XS_IMAGE_TAG)" && echo "OK: $(envget XS_IMAGE_TAG) healthy"
		;;
	status)
		echo "current:  $(envget XS_IMAGE_TAG)"
		echo "previous: $(envget XS_PREVIOUS_IMAGE_TAG)"
		echo "mode:     $(envget XS_DEPLOY_MODE)"
		dc ps
		dc exec -T server xs-ctl info
		;;
	*) die "unknown command '$cmd'" ;;
	esac
}

# ---- local side ----

usage() { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

[ "${1:-}" = --on-server ] && {
	shift
	on_server "$@"
	exit $?
}

cmd=${1:-}
[ -n "$cmd" ] || usage
host=${XS_DEPLOY_HOST:-}
[ -n "$host" ] || die "set XS_DEPLOY_HOST=user@host"
dir=${XS_DEPLOY_DIR:-crossstream}
case "$dir" in *[!A-Za-z0-9._/-]*) die "XS_DEPLOY_DIR: letters, digits, . _ / - only" ;; esac
mode=${XS_DEPLOY_MODE:-host}
case "$mode" in host | bridge) ;; *) die "XS_DEPLOY_MODE must be host or bridge" ;; esac
ssh=${XS_DEPLOY_SSH:-ssh -o BatchMode=yes}
here=$(cd "$(dirname "$0")" && pwd)

remote() { $ssh "$host" "$@"; } # $ssh unquoted on purpose: command plus options

case "$cmd" in
deploy)
	version=${2:-}
	[ -n "$version" ] || usage
	case "$version" in *[!A-Za-z0-9._-]*) die "version: letters, digits, . _ - only" ;; esac
	tarball=${3:-}
	[ -z "$tarball" ] || [ -f "$tarball" ] || die "$tarball not found"
	tar -cf - -C "$here" docker-compose.yml docker-compose.host.yml server.example.yaml \
		90-crossstream.conf remote-deploy.sh | remote "mkdir -p $dir && tar -xf - -C $dir" ||
		die "copying the compose files to $host:$dir failed"
	if [ -n "$tarball" ]; then
		echo "loading $(basename "$tarball") on $host"
		remote "docker load -q" <"$tarball" || die "docker load on $host failed"
	fi
	remote "cd $dir && XS_DEPLOY_MODE=$mode sh remote-deploy.sh --on-server deploy $version"
	;;
rollback | status | probe)
	remote "cd $dir && sh remote-deploy.sh --on-server $cmd"
	;;
*) usage ;;
esac
