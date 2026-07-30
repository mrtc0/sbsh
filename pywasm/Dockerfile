# syntax=docker/dockerfile:1

ARG WASI_SDK_VERSION=33
ARG CPYTHON_TAG=v3.14.6
ARG ZLIB_TAG=v1.3.2
ARG WASMTIME_VERSION=47.0.2

FROM debian:bookworm AS builder
ARG WASI_SDK_VERSION
ARG CPYTHON_TAG
ARG ZLIB_TAG
ARG TARGETARCH
ARG WASMTIME_VERSION

RUN apt-get update && apt-get install -y --no-install-recommends \
      git make curl ca-certificates xz-utils pkg-config zip \
      python3 build-essential libffi-dev zlib1g-dev libssl-dev \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) SDK_ARCH=x86_64 ;; \
      arm64) SDK_ARCH=arm64 ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}"; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/wasi-sdk.tar.gz \
      "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-${WASI_SDK_VERSION}/wasi-sdk-${WASI_SDK_VERSION}.0-${SDK_ARCH}-linux.tar.gz"; \
    mkdir -p /opt/wasi-sdk; \
    tar xzf /tmp/wasi-sdk.tar.gz -C /opt/wasi-sdk --strip-components=1; \
    rm /tmp/wasi-sdk.tar.gz
ENV WASI_SDK_PATH=/opt/wasi-sdk

RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) WT_ARCH=x86_64 ;; \
      arm64) WT_ARCH=aarch64 ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}"; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/wasmtime.tar.xz \
      "https://github.com/bytecodealliance/wasmtime/releases/download/v${WASMTIME_VERSION}/wasmtime-v${WASMTIME_VERSION}-${WT_ARCH}-linux.tar.xz"; \
    tar xf /tmp/wasmtime.tar.xz -C /tmp; \
    cp "/tmp/wasmtime-v${WASMTIME_VERSION}-${WT_ARCH}-linux/wasmtime" /usr/local/bin/wasmtime; \
    chmod +x /usr/local/bin/wasmtime; \
    rm -rf "/tmp/wasmtime.tar.xz" "/tmp/wasmtime-v${WASMTIME_VERSION}-${WT_ARCH}-linux"; \
    wasmtime --version

RUN set -eux; \
    git clone --depth 1 --branch "${ZLIB_TAG}" https://github.com/madler/zlib /tmp/zlib; \
    cd /tmp/zlib; \
    SYSROOT="$WASI_SDK_PATH/share/wasi-sysroot"; \
    CC="$WASI_SDK_PATH/bin/clang"; AR="$WASI_SDK_PATH/bin/llvm-ar"; \
    for f in adler32 compress crc32 deflate gzclose gzlib gzread gzwrite \
             infback inffast inflate inftrees trees uncompr zutil; do \
      "$CC" --sysroot="$SYSROOT" -O2 -DHAVE_UNISTD_H=1 -DHAVE_STDARG_H=1 -c "$f.c"; \
    done; \
    "$AR" rcs libz.a ./*.o; \
    cp -f zlib.h zconf.h "$SYSROOT/include/"; \
    for d in lib/wasm32-wasi lib/wasm32-wasip1; do \
      [ -d "$SYSROOT/$d" ] && cp -f libz.a "$SYSROOT/$d/" || true; \
    done; \
    mkdir -p "$SYSROOT/lib/pkgconfig"; \
    ZVER="$(sed -n 's/^#define ZLIB_VERSION "\(.*\)"/\1/p' zlib.h | head -1)"; \
    { \
      echo 'prefix=/'; \
      echo 'exec_prefix=${prefix}'; \
      echo 'libdir=${prefix}/lib/wasm32-wasip1'; \
      echo 'includedir=${prefix}/include'; \
      echo 'Name: zlib'; \
      echo 'Description: zlib compression library (wasm32-wasi build)'; \
      echo "Version: ${ZVER:-1.3.1}"; \
      echo 'Libs: -L${libdir} -lz'; \
      echo 'Cflags: -I${includedir}'; \
    } > "$SYSROOT/lib/pkgconfig/zlib.pc"

RUN set -eux; \
    git clone --depth 1 --branch "${CPYTHON_TAG}" https://github.com/python/cpython /tmp/cpython; \
    cd /tmp/cpython; \
    python3 Tools/wasm/wasi build -- --config-cache; \
    mkdir -p /out/lib; \
    printf '%s' "${CPYTHON_TAG#v}" > /out/PYTHON_VERSION; \
    # Three quarters of the linked module is DWARF, which nothing downstream reads.
    # The name section is kept, because it is what makes a wasm-level panic legible.
    "$WASI_SDK_PATH/bin/llvm-strip" --strip-debug \
      "$(find cross-build -name python.wasm | head -1)" -o /out/python.wasm; \
    # The standard library is committed as a plain tree rather than a zip. An
    # already-compressed archive is one opaque blob to git, so every CPython
    # update would add another copy of the whole thing; as files, the ones that
    # did not change are the same objects and cost nothing.
    cp -a Lib/. /out/lib/; \
    rm -rf /out/lib/test /out/lib/idlelib /out/lib/turtledemo; \
    rm -rf /out/lib/*/test /out/lib/*/tests; \
    # Drop the precompiled bytecode. A timestamp-based .pyc is accepted only when
    # it records its source's mtime, and the sandbox copies this tree into a fresh
    # in-memory filesystem that stamps every file at copy time, so not one of them
    # can ever match: CPython rejects all of them and compiles from source regardless.
    find /out/lib -type d -name __pycache__ -prune -exec rm -rf {} +; \
    # sbsh embeds python.wasm and this stdlib tree into its own binary, so every
    # sbsh distribution is a CPython distribution. The PSF License grants the
    # right to redistribute provided the License Agreement and PSF's copyright
    # notice are retained, and CPython's LICENSE carries both, along with the
    # BeOpen, CNRI and CWI agreements the same file preserves.
    #
    # LICENSE says only that "Some software incorporated into Python is under
    # different licenses"; Doc/license.rst is where those licenses are collected,
    # and the modules under them — libmpdec, SipHash, dtoa, the Mersenne Twister
    # among them — are compiled into python.wasm. zlib's own LICENSE is copied
    # separately because the module is linked against the build from madler/zlib
    # above, not against the copy CPython carries.
    mkdir -p /out/licenses; \
    cp /tmp/cpython/LICENSE /out/licenses/CPYTHON-LICENSE.txt; \
    cp /tmp/cpython/Doc/license.rst /out/licenses/CPYTHON-INCORPORATED-SOFTWARE.txt; \
    cp /tmp/zlib/LICENSE /out/licenses/ZLIB-LICENSE.txt; \
    for f in /out/licenses/*.txt; do test -s "$f"; done; \
    { \
      echo "cpython=${CPYTHON_TAG}"; \
      echo "wasi-sdk=${WASI_SDK_VERSION}"; \
      echo "zlib=${ZLIB_TAG}"; \
      echo "python.wasm.sha256=$(sha256sum /out/python.wasm | cut -d' ' -f1)"; \
    } > /out/PROVENANCE; \
    # Smoke-test the stripped module before it leaves the build. Running it here
    # means a strip that produced a broken module fails the build rather than
    # being committed. No "--" before -c: it would reach Python as an
    # end-of-options marker and turn -c into a file name.
    PYVER="$(cut -d. -f1,2 /out/PYTHON_VERSION)"; \
    wasmtime run --dir "/out/lib::/usr/lib/python${PYVER}" \
      --env "PYTHONHOME=/usr" --env "PYTHONPATH=/usr/lib/python${PYVER}" \
      --env PYTHONDONTWRITEBYTECODE=1 \
      /out/python.wasm -c 'import zlib, json, re; print("smoke ok", zlib.crc32(b"x"))'

FROM scratch AS artifacts
COPY --from=builder /out/ /
