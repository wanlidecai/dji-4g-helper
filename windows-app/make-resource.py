#!/usr/bin/env python3
"""Embed a 256px PNG icon in a Windows amd64 COFF resource, without windres.

The committed .syso is used by Go builds, so Pillow is needed only when updating
the brand icon. Resource IDs follow RT_ICON=3 and RT_GROUP_ICON=14, both ID=1.
Format: https://learn.microsoft.com/en-us/windows/win32/debug/pe-format
"""
import argparse
from pathlib import Path
import struct
from io import BytesIO

ROOT = Path(__file__).resolve().parent

def u32(value):
    return struct.pack('<I', value)

def directory(ids):
    return struct.pack('<IIHHHH', 0, 0, 0, 0, 0, len(ids)) + b''.join(
        struct.pack('<II', resource_id, offset) for resource_id, offset in ids)

def generate(source):
    from PIL import Image
    image = Image.open(source).convert('RGBA').resize((256, 256), Image.Resampling.LANCZOS)
    png_io = BytesIO()
    image.save(png_io, format='PNG')
    png = png_io.getvalue()
    group = struct.pack('<HHHBBBBHHIH', 0, 1, 1, 0, 0, 0, 0, 1, 32, len(png), 1)
    # Tree: root(32), type directories(24 each), language directories(24 each),
    # two data entries(16 each), then the raw payload. All offsets are .rsrc-relative.
    payload = 160
    group_offset = (payload + len(png) + 3) & ~3
    section = directory([(3, 0x80000020), (14, 0x80000038)])
    section += directory([(1, 0x80000050)]) + directory([(1, 0x80000068)])
    section += directory([(1033, 128)]) + directory([(1033, 144)])
    section += struct.pack('<IIII', payload, len(png), 0, 0)
    section += struct.pack('<IIII', group_offset, len(group), 0, 0)
    section += png + b'\0' * (group_offset - payload - len(png)) + group
    raw_offset = 60
    reloc_offset = raw_offset + len(section)
    # IMAGE_REL_AMD64_ADDR32NB links the two leaf pointers to the final .rsrc RVA.
    relocations = struct.pack('<IIHIIH', 128, 0, 3, 144, 0, 3)
    symbol_offset = reloc_offset + len(relocations)
    header = struct.pack('<HHIIIHH', 0x8664, 1, 0, symbol_offset, 1, 0, 0)
    section_header = struct.pack('<8sIIIIIIHHI', b'.rsrc', 0, 0, len(section), raw_offset,
                                 reloc_offset, 0, 2, 0, 0x40300040)
    symbol = struct.pack('<8sIhHBB', b'.rsrc', 0, 1, 0, 3, 0)
    (ROOT / 'app_windows_amd64.syso').write_bytes(header + section_header + section + relocations + symbol + u32(4))
    # The standalone icon is retained for inspection/reuse, and is embedded in PE.
    ico = struct.pack('<HHHBBBBHHII', 0, 1, 1, 0, 0, 0, 0, 1, 32, len(png), 22) + png
    (ROOT / 'app.ico').write_bytes(ico)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('png', type=Path)
    generate(parser.parse_args().png)
