"""Create local S3 service credentials without displaying secrets or replacing existing settings."""
import json
import os
from pathlib import Path
import secrets
import shlex

path = Path('.env')
values = {}
for line in path.read_text().splitlines():
    if '=' not in line or line.startswith('#'):
        continue
    key, raw = line.split('=', 1)
    try:
        decoded = json.loads(raw)
        values[key] = str(decoded).lower() if isinstance(decoded, (bool, int)) else decoded
    except ValueError:
        parsed = shlex.split(raw)
        values[key] = parsed[0] if parsed else ''
new_local_storage = not values.get('S3_ENDPOINT')
defaults = {
    'S3_ENDPOINT': 'localhost:9000', 'S3_BUCKET': 'pvzce-quarantine',
    'S3_ACCESS_KEY': secrets.token_hex(16), 'S3_SECRET_KEY': secrets.token_hex(32),
    'S3_SECURE': 'false', 'DOWNLOAD_HASH_KEY': secrets.token_hex(32),
    'CLAMD_ADDRESS': 'localhost:3310',
}
with path.open('a') as out:
    for key, value in defaults.items():
        if not values.get(key) or (key == 'S3_SECURE' and new_local_storage):
            out.write(f'\n{key}={shlex.quote(value)}\n')
            values[key] = value
os.chmod(path, 0o600)
config = Path('configs/s3.json')
config.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
config.write_text(json.dumps({'identities': [{'name': 'site-server', 'credentials': [{
    'accessKey': values['S3_ACCESS_KEY'], 'secretKey': values['S3_SECRET_KEY'],
}], 'actions': ['Admin', 'Read', 'Write', 'List', 'Tagging']}]}))
os.chmod(config, 0o600)
print('Local S3 credentials configured; no credentials displayed.')
