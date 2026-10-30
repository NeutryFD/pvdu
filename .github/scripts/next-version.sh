#!/usr/bin/env bash
# Prints the next release tag for the commits since the latest v* tag, or nothing
# when none of them warrants a release. Rules (Conventional Commits):
#   breaking change (type!: or BREAKING CHANGE)  -> major (minor while still 0.x)
#   feat                                         -> minor
#   fix, perf, build(deps), deps                 -> patch
#   anything else (docs, ci, chore, test, ...)   -> no release
set -euo pipefail

ref="${1:-HEAD}"

# Re-running on a commit that is already tagged must not publish twice.
if git tag --points-at "$ref" --list 'v[0-9]*' | grep -q .; then
  exit 0
fi

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*' "$ref" 2>/dev/null || true)
if [ -z "$last" ]; then
  echo "v0.1.0"
  exit 0
fi

major_minor_patch="${last#v}"
IFS=. read -r major minor patch <<<"$major_minor_patch"

level=none
raise() {
  case "$1:$level" in
    major:*) level=major ;;
    minor:none | minor:patch) level=minor ;;
    patch:none) level=patch ;;
  esac
}

# One record per commit: subject, then body, separated by a NUL.
while IFS= read -r -d '' commit; do
  commit="${commit#"${commit%%[!$'\n']*}"}" # git log separates records with a newline
  [ -n "$commit" ] || continue
  subject="${commit%%$'\n'*}"
  if [[ "$subject" =~ ^[a-z]+(\(.+\))?!: ]] || grep -q '^BREAKING CHANGE' <<<"$commit"; then
    raise major
  elif [[ "$subject" =~ ^feat(\(.+\))?: ]]; then
    raise minor
  elif [[ "$subject" =~ ^(fix|perf)(\(.+\))?: ]] || [[ "$subject" =~ ^(build\(deps|deps) ]]; then
    raise patch
  fi
done < <(git log --no-merges --format='%B%x00' "$last..$ref")

case "$level" in
  none) exit 0 ;;
  major)
    if [ "$major" -eq 0 ]; then
      echo "v0.$((minor + 1)).0"
    else
      echo "v$((major + 1)).0.0"
    fi
    ;;
  minor) echo "v$major.$((minor + 1)).0" ;;
  patch) echo "v$major.$minor.$((patch + 1))" ;;
esac
