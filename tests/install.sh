#!/usr/bin/env bash
# Offline installer checks. Optional argument: a real native filewise binary to install.
set -euo pipefail
umask 077
repo=$(cd "$(dirname "$0")/.." && pwd)
version=$(awk -F '"' '/^const version = / { print $2; exit }' "$repo/cmd/filewise/main.go")
work=$(mktemp -d "${TMPDIR:-/tmp}/filewise-installer-test.XXXXXX")
service_binary=''
cleanup() {
  if [[ -n "$service_binary" ]]; then
    "$service_binary" --db "$work/state/filewise.db" stop >/dev/null 2>&1 || {
      echo "Could not stop test service; inspect retained directory: $work" >&2; return 1;
    }
  fi
  rm -rf -- "$work"
}
trap cleanup EXIT
mkdir -p "$work/stubs" "$work/payload" "$work/assets" "$work/home"
export HOME="$work/home" FW_TEST_ASSETS="$work/assets" FW_TEST_CALLS="$work/calls"
export FW_TEST_VERSION="$version" FW_TEST_FAILURE=''
export FILEWISE_INSTALL_DIR="$work/install with spaces"
export PATH="$work/stubs:$PATH"
if [[ $# == 1 ]]; then
  cp "$1" "$work/payload/filewise"
else
  printf '#!/bin/sh\nprintf "filewise %s (Go)\\n"\n' "$version" > "$work/payload/filewise"
fi
chmod 755 "$work/payload/filewise"
cat > "$work/stubs/uname" <<'STUB'
#!/usr/bin/env bash
case "$1" in -s) echo "$FW_TEST_OS" ;; -m) echo "$FW_TEST_ARCH" ;; *) exit 1 ;; esac
STUB
cat > "$work/stubs/curl" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
out=''
url=${!#}
printf '%s\n' "$url" >> "$FW_TEST_CALLS"
while [[ $# -gt 0 ]]; do
  case "$1" in --output) out=$2; shift ;; esac
  shift
done
[[ "$FW_TEST_FAILURE" != network ]] || exit 22
if [[ "$url" == https://github.com/huaiwen/filewise/releases/latest ]]; then
  if [[ "$FW_TEST_FAILURE" == redirect ]]; then
    printf 'https://example.invalid/releases/tag/v%s' "$FW_TEST_VERSION"
  else
    printf 'https://github.com/huaiwen/filewise/releases/tag/v%s' "$FW_TEST_VERSION"
  fi
else
  [[ "$url" == "https://github.com/huaiwen/filewise/releases/download/v$FW_TEST_VERSION/"* ]] || exit 23
  name=${url##*/}
  [[ "$FW_TEST_FAILURE" != checksum || "$name" != *.sha256 ]] || exit 22
  cp "$FW_TEST_ASSETS/$name" "$out"
  if [[ "$FW_TEST_FAILURE" == corrupt && "$name" == *.tar.gz ]]; then printf tampered >> "$out"; fi
fi
STUB
for tool in go cargo rustc sudo python python3 node; do
  printf '#!/bin/sh\necho forbidden >> "$FW_TEST_CALLS"\nexit 99\n' > "$work/stubs/$tool"
done
chmod +x "$work/stubs/"*
checksum() {
  if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}
package() {
  local asset="filewise-$1.tar.gz"
  tar -czf "$work/assets/$asset" -C "$work/payload" filewise
  (cd "$work/assets" && checksum "$asset") > "$work/assets/$asset.sha256"
}
for row in 'Darwin arm64 darwin-arm64' 'Darwin x86_64 darwin-amd64' \
           'Linux x86_64 linux-amd64' 'Linux aarch64 linux-arm64'; do
  read -r FW_TEST_OS FW_TEST_ARCH target <<< "$row"
  export FW_TEST_OS FW_TEST_ARCH
  package "$target"
  bash "$repo/install.sh" > "$work/output"
  [[ $("$FILEWISE_INSTALL_DIR/filewise" --version) == "filewise $version (Go)" ]]
  cmp "$work/payload/filewise" "$FILEWISE_INSTALL_DIR/filewise"
  grep -F "/download/v$version/filewise-$target.tar.gz" "$FW_TEST_CALLS" >/dev/null
  echo "PASS: platform mapping and install $target (mock network/system detection)"
done
bash "$repo/install.sh" "v$version" > "$work/output"
if [[ $# == 1 ]]; then
  service_binary="$FILEWISE_INSTALL_DIR/filewise"
  "$service_binary" --db "$work/state/filewise.db" start --no-open --port 0 > "$work/start.json"
  "$service_binary" --db "$work/state/filewise.db" status > "$work/status.json"
  printf 'invalid PDF' | env -i "$service_binary" __document_worker pdf > "$work/worker.json"
  grep -E '"status":[[:space:]]*"failed"' "$work/worker.json" >/dev/null
  "$service_binary" --db "$work/state/filewise.db" stop > "$work/stop.json"
  grep -E '"stopped":[[:space:]]*true' "$work/stop.json" >/dev/null
  service_binary=''
  echo 'PASS: real installed service start/status/stop and native worker with empty environment'
fi
cp "$FILEWISE_INSTALL_DIR/filewise" "$work/before"
reject() {
  local label=$1 message=$2
  shift 2
  if bash "$repo/install.sh" "$@" > "$work/output" 2>&1; then
    echo "FAIL: $label unexpectedly succeeded" >&2; exit 1
  fi
  grep -F "$message" "$work/output" >/dev/null || { cat "$work/output"; exit 1; }
  cmp "$work/before" "$FILEWISE_INSTALL_DIR/filewise"
  [[ -z $(find "$FILEWISE_INSTALL_DIR" -name '.filewise-install.*' -print) ]]
  echo "PASS: $label; previous binary preserved, staging cleaned"
}
FW_TEST_FAILURE=network reject 'unavailable release' 'No published release'
FW_TEST_FAILURE=redirect reject 'unexpected latest URL' 'Unexpected latest-release URL'
FW_TEST_FAILURE=checksum reject 'missing checksum' 'Checksum download failed'
FW_TEST_FAILURE=corrupt reject 'corrupted download' 'SHA-256 mismatch'
reject 'invalid version' 'Version must look like' 'v0.3.0/../../other'
FW_TEST_VERSION=0.2.0 reject 'historical Rust release' 'No Go release selected'
! grep -F '/download/v0.2.0/' "$FW_TEST_CALLS"
FW_TEST_OS=Windows_NT reject 'unsupported OS' 'No prebuilt binary'
asset="filewise-$target.tar.gz"
printf extra > "$work/payload/extra"
tar -czf "$work/assets/$asset" -C "$work/payload" filewise extra
(cd "$work/assets" && checksum "$asset") > "$work/assets/$asset.sha256"
reject 'unexpected archive entry' 'Unexpected archive contents'
printf '#!/bin/sh\necho "filewise 999.0.0 (Go)"\n' > "$work/payload/filewise"
package "$target"
reject 'wrong binary version' 'Binary version does not match'
printf '#!/bin/sh\nprintf "filewise %s\\n"\n' "$version" > "$work/payload/filewise"
package "$target"
reject 'non-Go binary' 'Binary version does not match'
printf '#!/bin/sh\nexit 1\n' > "$work/payload/filewise"
package "$target"
reject 'unrunnable binary' 'Binary cannot run here'
cp "$work/before" "$work/payload/filewise"
package "$target"
printf '%064d  wrong-name\n' 0 > "$work/assets/$asset.sha256"
reject 'wrong checksum filename' 'Invalid checksum record'
package "$target"
mv "$FILEWISE_INSTALL_DIR/filewise" "$work/existing"
ln -s "$work/existing" "$FILEWISE_INSTALL_DIR/filewise"
reject 'symlink destination' 'Refusing to replace a symlink'
[[ -L "$FILEWISE_INSTALL_DIR/filewise" ]]
rm "$FILEWISE_INSTALL_DIR/filewise"
mkdir "$FILEWISE_INSTALL_DIR/filewise"
if bash "$repo/install.sh" > "$work/output" 2>&1; then exit 1; fi
grep -F 'not a regular file' "$work/output" >/dev/null
rmdir "$FILEWISE_INSTALL_DIR/filewise"
FILEWISE_INSTALL_DIR=relative bash "$repo/install.sh" > "$work/output" 2>&1 && exit 1
grep -F 'must be an absolute path' "$work/output" >/dev/null
unset FILEWISE_INSTALL_DIR
bash "$repo/install.sh" "v$version" > "$work/output"
cmp "$work/before" "$HOME/.local/bin/filewise"
bash "$repo/install.sh" --help > "$work/output"
# Exercise shasum on Linux too, without depending on whether sha256sum is installed.
mkdir "$work/fallback"
for tool in bash tar gzip mktemp mkdir rm mv chmod cp shasum; do
  ln -s "$(command -v "$tool")" "$work/fallback/$tool"
done
ln -s "$work/stubs/curl" "$work/fallback/curl"
ln -s "$work/stubs/uname" "$work/fallback/uname"
PATH="$work/fallback" bash "$repo/install.sh" "v$version" > "$work/output"
cmp "$work/before" "$HOME/.local/bin/filewise"
# GNU tar invokes gzip through PATH; fail before downloading when it is missing.
rm "$work/fallback/gzip"
cp "$FW_TEST_CALLS" "$work/calls-before"
if PATH="$work/fallback" bash "$repo/install.sh" "v$version" > "$work/output" 2>&1; then exit 1; fi
grep -F 'Missing required command: gzip' "$work/output" >/dev/null
cmp "$work/calls-before" "$FW_TEST_CALLS"
cmp "$work/before" "$HOME/.local/bin/filewise"
# Download interrupted inside main(): no network/filesystem install work should execute.
head -n 35 "$repo/install.sh" > "$work/truncated.sh"
if bash "$work/truncated.sh" > "$work/output" 2>&1; then exit 1; fi
cmp "$work/calls-before" "$FW_TEST_CALLS"
cmp "$work/before" "$HOME/.local/bin/filewise"
! grep -F forbidden "$FW_TEST_CALLS"
[[ ! -e "$HOME/.profile" && ! -e "$HOME/.bashrc" && ! -e "$HOME/.zshrc" && ! -e "$HOME/Library" ]]
echo 'PASS: pinned version, default destination, shasum fallback, interrupted script, non-file/relative targets, help, no compiler/sudo/profile/startup changes'
