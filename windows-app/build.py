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
APP_NAME='大疆4g模块辅助工具'
VERSION='1.2.2'
DEFAULT_OUTPUT=ROOT.parent / f'{APP_NAME}-Windows-x64-{VERSION}'

def build(go, dll, zadig=None, output=None, archive=None):
    output=(output or DEFAULT_OUTPUT).expanduser().resolve()
    archive=(archive or output.parent/(output.name+'.zip')).expanduser().resolve()
    if output == archive or output in archive.parents:
        raise ValueError('ZIP archive must be outside the packaged output directory')
    env=dict(os.environ,GOOS='windows',GOARCH='amd64',CGO_ENABLED='0')
    if not (ROOT/'app_windows_amd64.syso').is_file():
        raise FileNotFoundError('Missing embedded brand resource app_windows_amd64.syso')
    output.mkdir(parents=True,exist_ok=True)
    runtime=output/'runtime';runtime.mkdir(exist_ok=True)
    flags=['build','-trimpath','-ldflags=-H=windowsgui -s -w']
    subprocess.run([go,*flags,'-o',str(output/f'{APP_NAME}.exe'),'.'],cwd=ROOT,env=env,check=True)
    subprocess.run([go,*flags,'-o',str(runtime/'DJOneHub-backend.exe'),'./cmd/djonehub-macos'],cwd=ROOT/'backend',env=env,check=True)
    shutil.copy2(dll,runtime/'libusb-1.0.dll')
    shutil.copy2(ROOT/'backend/LICENSE',output/'LICENSE')
    shutil.copy2(ROOT/'THIRD_PARTY_NOTICES.md',output/'THIRD_PARTY_NOTICES.md')
    licenses=output/'licenses';licenses.mkdir(exist_ok=True)
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
    (output/'使用说明.txt').write_text((ROOT/'使用说明.txt').read_text(),encoding='utf-8-sig')
    if zadig:
        tools=output/'工具';tools.mkdir(exist_ok=True);shutil.copy2(zadig,tools/'Zadig-2.9.exe');shutil.copy2(ROOT/'zadig-COPYING',licenses/'zadig-COPYING')
    else:
        (output/'下载WinUSB驱动工具.url').write_text('[InternetShortcut]\nURL=https://zadig.akeo.ie/\n',encoding='utf-8-sig')
    files=[p for p in sorted(output.rglob('*')) if p.is_file() and p.name!='SHA256SUMS.txt']
    hashes='\n'.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.relative_to(output).as_posix()}' for p in files)+'\n'
    (output/'SHA256SUMS.txt').write_text(hashes)
    archive.parent.mkdir(parents=True,exist_ok=True)
    with zipfile.ZipFile(archive,'w',zipfile.ZIP_DEFLATED) as z:
        for p in sorted(output.rglob('*')):
            if p.is_file(): z.write(p,p.relative_to(output.parent))
    print(archive)
    print('SHA256',hashlib.sha256(archive.read_bytes()).hexdigest())
    return archive

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go',default=shutil.which('go'),required=shutil.which('go') is None)
    parser.add_argument('--libusb-dll',type=Path,required=True)
    parser.add_argument('--zadig',type=Path)
    parser.add_argument('--output-dir',type=Path,help='Packaged directory; defaults to the project name and version')
    parser.add_argument('--archive',type=Path,help='ZIP path outside the packaged directory')
    args=parser.parse_args()
    build(args.go,args.libusb_dll,args.zadig,args.output_dir,args.archive)
