#!/usr/bin/env python3
"""Build the optional ARM receiver with a legacy-kernel-compatible Go toolchain."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parent.parent

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', required=True, help='Path to Go 1.18–1.23 (1.23.12 recommended)')
    args = parser.parse_args()
    version = subprocess.check_output([args.go, 'version'], text=True)
    match = re.search(r'go1\.(\d+)', version)
    if not match or not 18 <= int(match.group(1)) <= 23:
        parser.error('Older Kobo kernels require Go 1.18–1.23; Go 1.24+ is not supported.')
    output = ROOT / 'dist'
    output.mkdir(exist_ok=True)
    shutil.copy2(ROOT/'kobo/README.md',output/'Pageport-Kobo-setup.md')
    binary = output / 'pageport-receiver'
    env = os.environ.copy()
    env.update(GOOS='linux', GOARCH='arm', GOARM='5', CGO_ENABLED='0')
    subprocess.run([args.go,'build','-trimpath','-ldflags=-s -w','-o',str(binary),'./cmd/pageport-kobo'],cwd=ROOT,env=env,check=True)
    files = [(binary,'.adds/pageport/pageport-receiver',0o755),
             (ROOT/'kobo/start.sh','.adds/pageport/start.sh',0o755),
             (ROOT/'kobo/nickelmenu.conf','.adds/nm/pageport',0o644),
             (ROOT/'kobo/README.md','README.md',0o644)]
    archive = output / 'Pageport-kobo-receiver.zip'
    with zipfile.ZipFile(archive,'w',compression=zipfile.ZIP_DEFLATED) as bundle:
        for source,name,mode in files:
            info=zipfile.ZipInfo.from_file(source,arcname=name)
            info.create_system=3
            info.external_attr=(0o100000 | mode)<<16
            info.compress_type=zipfile.ZIP_DEFLATED
            bundle.writestr(info,source.read_bytes())
    digest=hashlib.sha256(archive.read_bytes()).hexdigest()
    (output/'Pageport-kobo-receiver.sha256').write_text(digest+'  '+archive.name+'\n')
    print(version.strip())
    print('Created '+str(archive))

if __name__=='__main__': main()
