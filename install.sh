#!/usr/bin/env bash
# Download a published native binary; never compile or modify shell/startup settings.
# Keep execution until the final line so a truncated curl | bash cannot partly install.
set -euo pipefail

main() {
  if [[ ${1:-} == --help ]]; then
    printf '%s\n' 'Usage: bash install.sh [vVERSION]' \
      'Default: latest published release; FILEWISE_INSTALL_DIR defaults to ~/.local/bin.' \
      'Requires Bash, curl, tar, gzip, and sha256sum or shasum. No Rust or sudo.'
    return
  fi
  [[ $# -le 1 ]] || die 'Usage: bash install.sh [vVERSION]'
  local version=${1:-latest} target asset url expected filename extra actual reported
  local install_dir=${FILEWISE_INSTALL_DIR:-${HOME:?HOME is required}/.local/bin}
  local repo=https://github.com/huaiwen/filewise
  local -a hash_command
  case "$install_dir" in /*) ;; *) die 'FILEWISE_INSTALL_DIR must be an absolute path.' ;; esac
  for tool in curl tar gzip mktemp uname; do
    command -v "$tool" >/dev/null || die "Missing required command: $tool"
  done
  if command -v sha256sum >/dev/null; then
    hash_command=(sha256sum)
  elif command -v shasum >/dev/null; then
    hash_command=(shasum -a 256)
  else
    die 'Install sha256sum or shasum first.'
  fi
  case "$(uname -s):$(uname -m)" in
    Darwin:arm64|Darwin:aarch64) target=aarch64-apple-darwin ;;
    Darwin:x86_64) target=x86_64-apple-darwin ;;
    Linux:x86_64|Linux:amd64) target=x86_64-unknown-linux-gnu ;;
    Linux:aarch64|Linux:arm64) target=aarch64-unknown-linux-gnu ;;
    *) die 'No prebuilt binary for this system. See docs/install.md; no source build was attempted.' ;;
  esac
  if [[ "$version" == latest ]]; then
    # Resolve once: two /latest/download requests could straddle a new release.
    url=$(fetch --head --output /dev/null --write-out '%{url_effective}' "$repo/releases/latest") ||
      die "No published release found or GitHub is unreachable. See $repo/releases"
    [[ "$url" == "$repo/releases/tag/"* ]] || die 'Unexpected latest-release URL.'
    version=${url##*/}
  fi
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]] ||
    die 'Version must look like v0.2.0 or v0.2.0-rc.1.'
  asset="filewise-$target.tar.gz"
  url="$repo/releases/download/$version"
  [[ ! -L "$install_dir/filewise" ]] || die 'Refusing to replace a symlink; use a different FILEWISE_INSTALL_DIR.'
  [[ ! -e "$install_dir/filewise" || -f "$install_dir/filewise" ]] || die 'Installation target is not a regular file.'
  umask 077
  mkdir -p "$install_dir"
  install_dir=$(cd "$install_dir" && pwd -P)
  # Stage on the destination filesystem; a failed download/check never replaces the old binary.
  work=$(mktemp -d "$install_dir/.filewise-install.XXXXXX")
  trap 'rm -rf -- "$work"' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  printf 'Downloading Filewise %s (%s)...\n' "$version" "$target"
  fetch --max-filesize 134217728 --output "$work/$asset" "$url/$asset" ||
    die "Binary unavailable for $version/$target. See $repo/releases"
  fetch --max-filesize 1024 --output "$work/checksum" "$url/$asset.sha256" ||
    die 'Checksum download failed; existing installation was not changed.'
  read -r expected filename extra < "$work/checksum" || die 'Invalid checksum file.'
  [[ "$expected" =~ ^[0-9a-f]{64}$ && "$filename" == "$asset" && -z "$extra" ]] ||
    die 'Invalid checksum record.'
  actual=$("${hash_command[@]}" "$work/$asset")
  [[ "${actual%% *}" == "$expected" ]] || die 'SHA-256 mismatch; existing installation was not changed.'
  [[ $(tar -tzf "$work/$asset") == filewise ]] || die 'Unexpected archive contents.'
  # Extract only the expected entry to stdout, never archive paths onto the filesystem.
  tar -xOzf "$work/$asset" filewise > "$work/filewise"
  chmod 755 "$work/filewise"
  reported=$("$work/filewise" --version) ||
    die 'Binary cannot run here. Requires macOS 13+ or Linux glibc 2.35+. See docs/install.md.'
  [[ "$reported" == "filewise ${version#v}" ]] || die 'Binary version does not match the release.'
  [[ ! -L "$install_dir/filewise" && ! -d "$install_dir/filewise" ]] || die 'Installation target changed; refusing replacement.'
  mv -f "$work/filewise" "$install_dir/filewise"
  printf '\nInstalled %s at %s/filewise\n' "$reported" "$install_dir"
  printf 'Start: %q start\n' "$install_dir/filewise"
  printf 'To use filewise in this terminal: export PATH=%q:"$PATH"\n' "$install_dir"
  printf '%s\n' 'No service was started and no shell profile or login item was changed.' \
    'For upgrades, stop all Filewise instances before installing, then start them again.'
}

die() { printf 'Filewise installer: %s\n' "$*" >&2; exit 1; }
fetch() {
  curl -q --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --tlsv1.2 --connect-timeout 15 --max-time 180 --retry 2 "$@"
}
main "$@"
