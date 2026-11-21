#!/bin/sh
set -eu

if [ "$(id -u)" = "0" ]; then
	db_path="${DB_PATH:-sync.db}"
	db_dir=$(dirname "$db_path")

	chown -R ynabup:ynabup /app

	case "$db_dir" in
		/app|/app/*|/data|/data/*)
			mkdir -p "$db_dir"
			chown -R ynabup:ynabup "$db_dir"
			;;
		.)
			;;
		*)
			echo "warning: DB_PATH directory $db_dir is outside /app or /data; skipping ownership adjustment" >&2
			;;
	esac

	exec su-exec ynabup /app/ynabup "$@"
fi

exec /app/ynabup "$@"
