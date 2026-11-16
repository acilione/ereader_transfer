#!/usr/bin/env python3
"""Run browser suites against isolated instances of a packaged executable."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from portable_smoke import start, stop

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('binary',type=Path)
parser.add_argument('--node',default='node')
args=parser.parse_args()
root=Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(prefix='pageport-browser-') as temp:
    binary=Path(temp)/args.binary.name
    shutil.copy2(args.binary.resolve(),binary)
    binary.chmod(0o755)
    for name in ['Pageport-kobo-receiver.zip','Pageport-Kobo-setup.md']:
        source=args.binary.resolve().parent/name
        if source.exists(): shutil.copy2(source,Path(temp)/name)
    for suite in ['browser.cjs','direct_browser.cjs']:
        process,base=start(binary,Path(temp)/suite)
        try:
            env=os.environ.copy()
            env['PAGEPORT_URL']=base
            env['PAGEPORT_QA_DIR']=str(root/'.qa'/('v0.2-'+suite))
            subprocess.run([args.node,str(root/'tests'/suite)],cwd=root,env=env,check=True)
        finally: stop(process)
