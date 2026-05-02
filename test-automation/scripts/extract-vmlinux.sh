#!/bin/bash
# Extract ELF vmlinux kernel from compressed bzImage
#
# Firecracker requires an uncompressed ELF kernel. Modern Linux kernels
# are distributed as compressed bzImage files in /boot/vmlinuz-*.
# This script extracts the raw ELF vmlinux for Firecracker use.
#
# Usage:
#   ./extract-vmlinux.sh [vmlinuz-path] [output-path]
#
# Defaults:
#   vmlinuz-path: /boot/vmlinuz-* (first match)
#   output-path:  ./vmlinux

set -e

VMLINUZ="${1:-/boot/vmlinuz-*}"
OUTPUT="${2:-./vmlinux}"

# Find actual vmlinuz if wildcard
if [[ "$VMLINUX" == *"*"* ]]; then
    VMLINUZ=$(ls $VMLINUX 2>/dev/null | head -1)
    if [ -z "$VMLINUZ" ]; then
        echo "ERROR: No vmlinuz found matching pattern"
        exit 1
    fi
fi

echo "Extracting vmlinux from: $VMLINUZ"
echo "Output: $OUTPUT"

# Detect compression format
# gzip: 1f 8b 08
# xz: fd 37 7a 58 5a 00
# bzip2: 42 5a 68
# lzma: 5d 00 00
# zstd: 28 b5 2f fd

PYTHON_SCRIPT='
import sys, lzma, gzip, bz2, struct

with open(sys.argv[1], "rb") as f:
    data = f.read()

# Try each compression format at known offsets
formats = [
    ("xz/lzma", b"\xfd7zXZ\x00", lzma.decompress),
    ("gzip", b"\x1f\x8b\x08", gzip.decompress),
    ("bzip2", b"BZh", bz2.decompress),
]

for name, magic, decompress in formats:
    offset = data.find(magic)
    if offset >= 0:
        print(f"Found {name} at offset {offset}")
        try:
            result = decompress(data[offset:])
            with open(sys.argv[2], "wb") as f:
                f.write(result)
            print(f"Success: {len(result)} bytes written")
            sys.exit(0)
        except Exception as e:
            print(f"Failed: {e}")
            continue

print("ERROR: No supported compression found")
sys.exit(1)
'

python3 -c "$PYTHON_SCRIPT" "$VMLINUZ" "$OUTPUT"

# Verify output
if ! file "$OUTPUT" | grep -q "ELF"; then
    echo "ERROR: Output is not ELF format"
    rm -f "$OUTPUT"
    exit 1
fi

echo "Kernel extracted successfully:"
file "$OUTPUT"
ls -la "$OUTPUT"