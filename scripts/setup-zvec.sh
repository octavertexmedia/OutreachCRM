#!/usr/bin/env bash
# Fetch Alibaba Zvec Go SDK + prebuilt native libs for -tags zvec builds.
# See https://github.com/alibaba/zvec and https://github.com/zvec-ai/zvec-go
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VER="${ZVEC_VERSION:-v0.5.1}"
DEST="${ROOT}/third_party/zvec-go"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "${OS}-${ARCH}" in
  linux-x86_64|linux-amd64) PLATFORM=linux-amd64; LIBDIR=linux_amd64 ;;
  linux-aarch64|linux-arm64) PLATFORM=linux-arm64; LIBDIR=linux_arm64 ;;
  darwin-arm64) PLATFORM=darwin-arm64; LIBDIR=darwin_arm64 ;;
  *)
    echo "Unsupported platform: ${OS}-${ARCH}" >&2
    exit 1
    ;;
esac

if [[ -f "${DEST}/go.mod" && ( -f "${DEST}/lib/${LIBDIR}/libzvec_c_api.dylib" || -f "${DEST}/lib/${LIBDIR}/libzvec_c_api.so" ) ]]; then
  echo "OK: Zvec already set up at ${DEST}"
  exit 0
fi

echo "→ cloning zvec-go ${VER} into third_party/zvec-go"
rm -rf "${DEST}"
git clone --depth 1 --branch "${VER}" https://github.com/zvec-ai/zvec-go.git "${DEST}"

echo "→ downloading ${PLATFORM} libs"
if ( cd "${DEST}" && go run ./cmd/download-libs -version "${VER}" ); then
  :
else
  TMP="$(mktemp -d)"
  curl -fsSL "https://github.com/zvec-ai/zvec-go/releases/download/${VER}/zvec-libs-${PLATFORM}.tar.gz" -o "${TMP}/libs.tgz"
  mkdir -p "${DEST}/lib"
  tar -xzf "${TMP}/libs.tgz" -C "${DEST}/lib"
  rm -rf "${TMP}"
fi

if [[ ! -f "${DEST}/lib/${LIBDIR}/libzvec_c_api.dylib" && ! -f "${DEST}/lib/${LIBDIR}/libzvec_c_api.so" ]]; then
  echo "ERROR: native lib missing under ${DEST}/lib/${LIBDIR}" >&2
  exit 1
fi

echo "OK: Zvec ready at ${DEST}"
echo "Build with: make build"
echo "Run with:   make run"
