#!/usr/bin/env python3
"""Build/package genuine x64 Windows binaries. Use --go and --libusb-dll.
Requires Go >=1.26.3, official libusb 1.0 DLL for Windows x64, optional Zadig.
Does not install drivers, mutate macOS apps, or launch the backend.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import zipfile

ROOT=Path(__file__).resolve().parent
OUTPUT=ROOT.parent / '碗里的菜-Windows-x64-1.2.1'

def build(go, dll, zadig=None):
    env=dict(os.environ,GOOS='windows',GOARCH='amd64',CGO_ENABLED='0')
    if not (ROOT/'app_windows_amd64.syso').is_file():
        raise FileNotFoundError('Missing embedded brand resource app_windows_amd64.syso')
    OUTPUT.mkdir(exist_ok=True)
    runtime=OUTPUT/'runtime';runtime.mkdir(exist_ok=True)
    flags=['build','-trimpath','-ldflags=-H=windowsgui -s -w']
    subprocess.run([go,*flags,'-o',str(OUTPUT/'碗里的菜.exe'),'.'],cwd=ROOT,env=env,check=True)
    subprocess.run([go,*flags,'-o',str(runtime/'DJOneHub-backend.exe'),'./cmd/djonehub-macos'],cwd=ROOT/'backend',env=env,check=True)
    shutil.copy2(dll,runtime/'libusb-1.0.dll')
    shutil.copy2(ROOT/'backend/LICENSE',OUTPUT/'LICENSE')
    shutil.copy2(ROOT/'THIRD_PARTY_NOTICES.md',OUTPUT/'THIRD_PARTY_NOTICES.md')
    licenses=OUTPUT/'licenses';licenses.mkdir(exist_ok=True)
    for existing in licenses.iterdir():
        if existing.is_file(): existing.chmod(existing.stat().st_mode | 0o200)
    libusb_license=ROOT/'libusb-COPYING'
    if not libusb_license.is_file(): raise FileNotFoundError(libusb_license)
    shutil.copy2(libusb_license,licenses/'libusb-COPYING')
    shutil.copy2(Path(subprocess.check_output([go,'env','GOROOT'],text=True).strip())/'LICENSE',licenses/'go-runtime-LICENSE')
    module_text=subprocess.check_output([go,'list','-m','-json','all'],cwd=ROOT/'backend',env=env,text=True)
    decoder=json.JSONDecoder()
    while module_text.strip():
        module,module_end=decoder.raw_decode(module_text.lstrip())
        module_text=module_text.lstrip()[module_end:]
        directory=module.get('Replace',module).get('Dir')
        if not directory: continue
        source=Path(directory)
        for filename in ['LICENSE','LICENSE.txt','LICENSE.md','COPYING','NOTICE']:
            item=source/filename
            if item.is_file():
                safe_name=module['Path'].replace('/','__')+'__'+filename
                shutil.copy2(item,licenses/safe_name)
    (OUTPUT/'使用说明.txt').write_text((ROOT/'使用说明.txt').read_text(),encoding='utf-8-sig')
    if zadig:
        tools=OUTPUT/'工具';tools.mkdir(exist_ok=True);shutil.copy2(zadig,tools/'Zadig-2.9.exe');shutil.copy2(ROOT/'zadig-COPYING',licenses/'zadig-COPYING')
    else:
        (OUTPUT/'下载WinUSB驱动工具.url').write_text('[InternetShortcut]\nURL=https://zadig.akeo.ie/\n',encoding='utf-8-sig')
    files=[p for p in sorted(OUTPUT.rglob('*')) if p.is_file() and p.name!='SHA256SUMS.txt']
    hashes='\n'.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.relative_to(OUTPUT).as_posix()}' for p in files)+'\n'
    (OUTPUT/'SHA256SUMS.txt').write_text(hashes)
    archive=OUTPUT.parent/(OUTPUT.name+'.zip')
    with zipfile.ZipFile(archive,'w',zipfile.ZIP_DEFLATED) as z:
        for p in sorted(OUTPUT.rglob('*')):
            if p.is_file(): z.write(p,p.relative_to(OUTPUT.parent))
    print(archive)
    print('SHA256',hashlib.sha256(archive.read_bytes()).hexdigest())

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go',default=shutil.which('go'),required=shutil.which('go') is None)
    parser.add_argument('--libusb-dll',type=Path,required=True)
    parser.add_argument('--zadig',type=Path)
    args=parser.parse_args()
    build(args.go,args.libusb_dll,args.zadig)
