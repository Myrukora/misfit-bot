#!/bin/sh
# Print the release version from the VERSION file: the first non-blank,
# non-comment line, trimmed of surrounding whitespace, validated as a bare
# semantic version (major.minor.patch[-prerelease], no build metadata).
#
# This is the ONE contract for reading VERSION. install.sh, CI and the release
# workflow all read it through here, and updater.ReadVersionFile +
# updater.ParseVersion (Go, used when the updater rebuilds itself) implement the
# same rule — the failure mode to avoid is a tag, a stamped binary and the
# release workflow disagreeing about what VERSION says, or a typo stamping a
# value no parser accepts.
#
# Exits non-zero with a message when the file is missing, holds no version, or
# its version is not valid SemVer, so a build refuses to run rather than ship
# garbage that every consumer then reports differently. Use -e to extract
# without validating.
#
# Usage: version.sh [-e] [path-to-VERSION]
set -eu

validate=1
if [ "${1:-}" = "-e" ]; then
	validate=0
	shift
fi

file=${1:-VERSION}
if [ ! -f "$file" ]; then
	echo "version.sh: no VERSION file at $file" >&2
	exit 1
fi

# First non-blank, non-comment line, trimmed.
version=$(awk '
	{
		line = $0
		gsub(/^[ \t\r]+/, "", line)
		gsub(/[ \t\r]+$/, "", line)
		if (line == "" || line ~ /^#/) next
		print line
		exit
	}' "$file")

if [ -z "$version" ]; then
	echo "version.sh: '$file' holds no version (blank or comments only)" >&2
	exit 1
fi

# The official SemVer grammar, "v" prefix tolerated, build metadata rejected.
# The generic prerelease alternative must contain a letter or hyphen, so
# zero-padded numbers ("-01") and empty components ("-rc..1") fail here exactly
# as they do in updater.ParseVersion.
if [ "$validate" = 1 ] && ! printf '%s' "$version" |
	grep -Eq '^(v?)(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?$'; then
	echo "version.sh: '$file' does not hold a bare SemVer (major.minor.patch[-prerelease]); found '$version'" >&2
	exit 1
fi

# Canonical for stamping: the "v" prefix belongs to tags, not to builds.
printf '%s\n' "${version#v}"
