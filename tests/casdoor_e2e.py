"""Run the real local Casdoor authorization-code flow without printing credentials.

Requires the running Compose stack and the standard Python 3 library only.
Uses the dedicated bootstrap demo user. Set CASDOOR_E2E_PASSWORD after changing it.
"""
import http.cookiejar
import json
import os
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def configuration():
    values = {}
    for line in Path('.env').read_text().splitlines():
        if '=' not in line or line.startswith('#'):
            continue
        key, value = line.split('=', 1)
        try:
            value = json.loads(value)
        except (ValueError, TypeError):
            value = value.strip("'\"")
        values[key] = value
    return values


def login(client, values, username="demo", password=None):
    app = values.get('APP_PUBLIC_URL', 'http://localhost:8080')
    issuer = values.get('CASDOOR_ISSUER_URL', 'http://localhost:8000')

    def send(url, data=None, headers=None):
        try:
            return client.open(urllib.request.Request(url, data=data, headers=headers or {}), timeout=12)
        except urllib.error.HTTPError as error:
            return error

    with send(app + '/auth/login') as response:
        assert response.status == 302, 'Login did not redirect to Casdoor'
        location = response.headers['Location']
    params = urllib.parse.parse_qs(urllib.parse.urlsplit(location).query)
    query = {target: params.get(source, [''])[0] for source, target in [
        ('client_id', 'clientId'), ('response_type', 'responseType'),
        ('redirect_uri', 'redirectUri'), ('scope', 'scope'), ('state', 'state'),
        ('nonce', 'nonce'), ('code_challenge', 'code_challenge'),
        ('code_challenge_method', 'code_challenge_method'),
    ]}
    body = {
        'application': values.get('CASDOOR_APPLICATION', 'pvzce-site'),
        'organization': 'pvzce', 'username': username,
        'password': password or os.environ.get('CASDOOR_E2E_PASSWORD', values['CASDOOR_DEMO_PASSWORD']),
        'type': 'code', 'signinMethod': 'Password', 'autoSignin': True, 'language': 'zh',
    }
    with send(issuer + '/api/login?' + urllib.parse.urlencode(query), json.dumps(body).encode(),
              {'Content-Type': 'application/json'}) as response:
        result = json.load(response)
    assert result.get('status') == 'ok', 'Casdoor rejected the bootstrap demo login'
    assert isinstance(result.get('data'), str), 'Casdoor did not return an authorization code'
    callback = app + '/auth/callback?' + urllib.parse.urlencode({
        'state': params['state'][0], 'code': result['data'],
    })
    with send(callback) as response:
        assert response.status == 303, 'Project callback rejected the authorization code'
        cookies = response.headers.get_all('Set-Cookie') or []
        assert any('pvzce_session=' in cookie and 'HttpOnly' in cookie and 'SameSite=Lax' in cookie
                   for cookie in cookies), 'Session cookie properties are incorrect'
    with send(app + '/api/v1/account/me') as response:
        assert response.status == 200, 'Account session is unavailable'
        session = json.load(response)['data']
    assert session['user']['username'] == username, 'Wrong identity returned by the account API'
    return send, session, callback


def main():
    values = configuration()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), NoRedirect())
    send, session, callback = login(client, values)
    app = values.get('APP_PUBLIC_URL', 'http://localhost:8080')
    with send(callback) as response:
        assert response.status == 401, 'Authorization callback replay was accepted'
    with send(app + '/auth/logout', b'', {'Origin': app, 'Content-Type': 'application/json'}) as response:
        assert response.status == 403, 'Logout without a CSRF token was accepted'
    with send(app + '/auth/logout', b'', {
        'Origin': app, 'Content-Type': 'application/json', 'X-CSRF-Token': session['csrf_token'],
    }) as response:
        assert response.status == 204, 'Logout failed'
    with send(app + '/api/v1/account/me') as response:
        assert response.status == 401, 'Revoked session still has access'
    print('Real Casdoor login, cookie session, callback replay prevention, CSRF and logout passed')


if __name__ == '__main__':
    main()
