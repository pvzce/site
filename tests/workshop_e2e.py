"""Real local Casdoor + PostgreSQL + S3 + ClamAV publication acceptance.

Run --prepare once, bootstrap the printed owner ID using the local operator command,
then run this script. A disposable local stack is required; never point at production.
No credentials, session cookies or upload bodies are printed. Fixture credentials stay
in ignored work/ with mode 0600. All real package bytes are generated in memory.
"""
import hashlib
import http.cookiejar
import io
import json
from pathlib import Path
import secrets
import sys
import urllib.parse
import urllib.request
import zipfile

from casdoor_e2e import NoRedirect, configuration, login


def client():
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), NoRedirect())


def main():
    values = configuration()
    app = values.get('APP_PUBLIC_URL', 'http://localhost:8080')
    issuer = values.get('CASDOOR_ISSUER_URL', 'http://localhost:8000')
    assert urllib.parse.urlsplit(app).hostname in ('localhost', '127.0.0.1'), 'Use a disposable local stack'
    actor_send, actor, _ = login(client(), values)
    state_path = Path('work/workshop-e2e.json')
    state_path.parent.mkdir(exist_ok=True)
    if '--prepare' in sys.argv:
        username = 'e2e-reviewer-' + secrets.token_hex(5)
        password = secrets.token_urlsafe(24)
        body = {'application': values.get('CASDOOR_APPLICATION', 'pvzce-site'),
                'organization': 'pvzce', 'username': username, 'name': 'Integration reviewer',
                'password': password, 'confirm': password, 'email': '', 'language': 'en'}
        request = urllib.request.Request(issuer + '/api/signup', data=json.dumps(body).encode(),
                                         headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=15) as response:
            result = json.load(response)
        assert result.get('status') == 'ok', 'Real Casdoor signup failed'
        state_path.write_text(json.dumps({'username': username, 'password': password,
                                        'owner_id': actor['user']['id']}))
        state_path.chmod(0o600)
        print(f"Real signup passed. Bootstrap local site admin for owner ID {actor['user']['id']} before continuing.")
        return
    state = json.loads(state_path.read_text())
    reviewer_send, reviewer, _ = login(client(), values, state['username'], state['password'])

    def api(send, session, method, path, body=None, expected=200):
        headers = {'Content-Type': 'application/json', 'X-CSRF-Token': session['csrf_token'], 'Origin': app}
        data = json.dumps(body or {}).encode() if method != 'GET' else None
        request = urllib.request.Request(app + path, data=data, method=method, headers=headers)
        # login.send intentionally preserves 303/HTTP errors through its no-redirect client.
        with send(request.full_url, request.data, dict(request.header_items())) as response:
            # send selects POST when data exists. Explicit PATCH isn't needed by this test.
            assert response.status == expected, f'{path}: HTTP {response.status}, expected {expected}'
            result = json.load(response) if response.status == 200 else None
            return result.get('data') if result else None

    profile = api(actor_send, actor, 'GET', '/api/v1/profile')
    assert profile['role'] == 'admin', 'Bootstrap the local owner using bin/server bootstrap-admin USER_ID'
    api(actor_send, actor, 'POST', f"/api/v1/admin/accounts/{reviewer['user']['id']}",
        {'role': 'reviewer', 'status': 'active', 'reason': 'Local end-to-end verification'})
    csrf = actor['csrf_token']

    def form(path, fields, expected=303):
        body = urllib.parse.urlencode({'csrf_token': csrf, **fields}).encode()
        with actor_send(app + path, body, {'Content-Type': 'application/x-www-form-urlencoded', 'Origin': app}) as response:
            assert response.status == expected, f'Form {path}: HTTP {response.status}'
            return response.headers.get('Location')

    slug = 'e2e-' + secrets.token_hex(8)
    location = form('/projects/new', {'slug': slug, 'title': 'Real publication verification', 'kind': 'datapack',
                                     'description': '<script>alert("escape")</script> verified package',
                                     'license': 'MIT', 'tags': 'verification', 'content_label': 'general'})
    pid = int(location.rsplit('/', 1)[1])
    form(f'/projects/{pid}/versions', {'number': '1.0.0', 'channel': 'stable', 'changelog': 'Real acceptance',
                                      'game_versions': '1.0.0', 'dependencies': '[]'})
    detail = api(actor_send, actor, 'GET', f'/api/v1/projects/{pid}')
    vid = detail['versions'][0]['id']
    with actor_send(app + f'/projects/{pid}') as response:
        html = response.read().decode()
    assert '&lt;script&gt;' in html and '<script>alert(' not in html, 'Template escaped content incorrectly'

    def package(infected=False):
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, 'w') as archive:
            archive.writestr('pack.mcmeta', json.dumps({'pack': {'pack_format': 1, 'description': 'PVZCE acceptance'}}))
            archive.writestr('data/example/plants/test.json', '{"health":300,"capabilities":[]}')
            if infected:
                archive.writestr('eicar.txt', 'X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*')
        return buffer.getvalue()

    def upload(version_id, content):
        boundary = 'pvzce-' + secrets.token_hex(16)
        fields = {'csrf_token': csrf, 'key': secrets.token_hex(16), 'purpose': 'package',
                  'version_id': str(version_id), 'size': str(len(content)), 'sha256': hashlib.sha256(content).hexdigest()}
        parts = [f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode()
                 for key, value in fields.items()]
        parts += [f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="fixture.zip"\r\nContent-Type: application/zip\r\n\r\n'.encode(), content,
                  f'\r\n--{boundary}--\r\n'.encode()]
        with actor_send(app + f'/projects/{pid}/upload', b''.join(parts),
                        {'Content-Type': 'multipart/form-data; boundary=' + boundary, 'Origin': app}) as response:
            assert response.status == 200, f'Ordinary multipart form upload failed: HTTP {response.status}'
            assert '上传结果' in response.read().decode(), 'Upload result page not rendered'
        current = api(actor_send, actor, 'GET', f'/api/v1/projects/{pid}')
        return next(file for file in current['files'] if file['version_id'] == version_id)

    content = package()
    file = upload(vid, content)
    assert file['scan_state'] == 'clean' and file['validation_state'] == 'valid', 'Real clean chain failed'
    with actor_send(app + f"/api/v1/files/{file['id']}/download") as response:
        assert response.status == 404, 'Unpublished file leaked'
    form(f'/projects/{pid}/transition', {'version_id': 0, 'action': 'submit', 'reason': 'Review project'})
    form(f'/projects/{pid}/transition', {'version_id': vid, 'action': 'submit', 'reason': 'Review version'})
    api(actor_send, actor, 'POST', f'/api/v1/projects/{pid}/transitions', {'action': 'approve', 'reason': 'self'}, 403)
    with reviewer_send(app + '/review') as response:
        assert response.status == 200 and str(pid).encode() in response.read(), 'Review queue missing project'
    with reviewer_send(app + f"/api/v1/review/files/{file['id']}") as response:
        assert response.status == 200 and hashlib.sha256(response.read()).hexdigest() == file['sha256'], 'Reviewer inspection mismatch'
    api(reviewer_send, reviewer, 'POST', f'/api/v1/projects/{pid}/transitions', {'action': 'approve', 'reason': 'Independent review'})
    api(reviewer_send, reviewer, 'POST', f'/api/v1/projects/{pid}/versions/{vid}/transitions', {'action': 'approve', 'reason': 'Independent version review'})
    for _ in range(2):
        with urllib.request.urlopen(app + f"/api/v1/files/{file['id']}/download", timeout=15) as response:
            assert response.status == 200 and hashlib.sha256(response.read()).hexdigest() == hashlib.sha256(content).hexdigest()
            assert response.headers['X-Checksum-SHA256'] == file['sha256']
    public = api(actor_send, actor, 'GET', f'/api/v1/projects/{pid}')
    assert public['project']['downloads'] == 1 and public['versions'][0]['downloads'] == 1, 'Download dedup failed'
    form(f'/projects/{pid}/transition', {'version_id': vid, 'action': 'withdraw', 'reason': 'Author withdrawal'})
    with actor_send(app + f"/api/v1/files/{file['id']}/download") as response:
        assert response.status == 404, 'Withdrawn file leaked'
    form(f'/projects/{pid}/transition', {'version_id': vid, 'action': 'resume', 'reason': 'Resume draft'})
    form(f'/projects/{pid}/transition', {'version_id': vid, 'action': 'submit', 'reason': 'Submit again'})
    api(reviewer_send, reviewer, 'POST', f'/api/v1/projects/{pid}/versions/{vid}/transitions', {'action': 'approve', 'reason': 'Review after withdrawal'})

    bad_version = api(actor_send, actor, 'POST', f'/api/v1/projects/{pid}/versions',
                      {'number': '2.0.0', 'channel': 'beta', 'changelog': 'EICAR fixture', 'game_versions': ['1.0.0'], 'dependencies': []})['id']
    infected = upload(bad_version, package(infected=True))
    assert infected['scan_state'] == 'infected', 'Real EICAR upload was not quarantined'
    api(actor_send, actor, 'POST', f'/api/v1/projects/{pid}/versions/{bad_version}/transitions', {'action': 'submit', 'reason': 'Must fail'}, 400)
    with actor_send(app + f"/api/v1/files/{infected['id']}/download") as response:
        assert response.status == 404, 'Malicious file leaked'
    api(reviewer_send, reviewer, 'POST', f'/api/v1/projects/{pid}/versions/{bad_version}/transitions', {'action': 'remove', 'reason': 'Malware fixture'})
    api(reviewer_send, reviewer, 'POST', f'/api/v1/projects/{pid}/versions/{bad_version}/transitions', {'action': 'restore', 'reason': 'Must not clear malware'}, 400)
    usage = api(actor_send, actor, 'GET', '/api/v1/upload-usage')
    assert usage['used'] >= len(content) and usage['reserved'] == 0 and usage['timezone'] == 'UTC'
    report = {'project_id': pid, 'version_id': vid, 'file_id': file['id'], 'sha256': file['sha256'],
              'real_services': ['Casdoor signup/login', 'PostgreSQL', 'S3 private bucket', 'ClamAV clean+EICAR'],
              'checks': ['ordinary forms', 'CSRF', 'ownership', 'independent review', 'quarantine', 'hash', 'dedup', 'withdraw/resume', 'malware restore denial']}
    Path('work/workshop-e2e-result.json').write_text(json.dumps(report, indent=2))
    print('Real publication E2E passed: forms → upload → independent review → download/hash; EICAR quarantined and restore denied.')
    print('SHA-256: ' + file['sha256'])


if __name__ == '__main__':
    main()
