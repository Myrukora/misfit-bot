#!/bin/sh
# Print the release version from the VERSION file: the first non-blank,
# non-comment line, trimmed of surrounding whitespace.
#
# This is the ONE parsing contract for the version. install.sh, CI and the
# release workflow all read VERSION through here, and updater.ReadVersionFile
# (Go, used when the updater rebuilds itself) implements the same rule — the
# failure mode to avoid is a tag, a stamped binary and the release workflow
# disagreeing about what VERSION says.
#
# Usage: version.sh [path-to-VERSION]
set -eu

file=${1:-VERSION}
if [ ! -f "$file" ]; then
	echo "version.sh: no VERSION file at $file" >&2
	exit 1
fi

awk '{
		line = $0
		gsub(/^[ \t\r]+/, "", line)
		gsub(/[ \t\r]+$/, "", line)
		if (line == "" || line ~ /^#/) next
		print line
		exit
	}' "$file"
