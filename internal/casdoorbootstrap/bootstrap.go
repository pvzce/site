// Package casdoorbootstrap prepares a local Casdoor deployment without network downloads.
package casdoorbootstrap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Initialize preserves existing database credentials, signing keys, and bootstrap passwords.
func Initialize(envPath, directory string) error {
	body, err := os.ReadFile(envPath)
	if os.IsNotExist(err) {
		body, err = os.ReadFile(".env.example")
	}
	if err != nil {
		return err
	}
	values, lines := parseEnvironment(string(body))
	original := make(map[string]string, len(values))
	for key, value := range values {
		original[key] = value
	}
	for key, fallback := range map[string]string{
		"APP_PUBLIC_URL": "http://localhost:8080", "CASDOOR_ISSUER_URL": "http://localhost:8000",
		"CASDOOR_APPLICATION": "pvzce-site", "CASDOOR_CLIENT_ID": "pvzce-site",
		"DB_USER": "pvzce", "DB_NAME": "pvzce", "AUTH_SESSION_TTL": "24h",
	} {
		if values[key] == "" {
			values[key] = fallback
		}
	}
	for _, key := range []string{"APP_PUBLIC_URL", "CASDOOR_ISSUER_URL"} {
		u, err := url.Parse(values[key])
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid %s", key)
		}
		values[key] = strings.TrimRight(values[key], "/")
	}
	if strings.ContainsAny(values["DB_USER"]+values["DB_PASSWORD"], "\r\n") {
		return fmt.Errorf("database credentials must not contain newlines")
	}
	if values["DB_PASSWORD"] == "" {
		values["DB_PASSWORD"] = rand.Text() + rand.Text()
	}
	seedPath := filepath.Join(directory, "init_data.json")
	seed := map[string]any{}
	if existing, err := os.ReadFile(seedPath); err == nil {
		if err := json.Unmarshal(existing, &seed); err != nil {
			return fmt.Errorf("existing Casdoor seed is invalid")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if values["CASDOOR_CLIENT_SECRET"] == "" {
		if apps, ok := seed["applications"].([]any); ok {
			for _, raw := range apps {
				if app, ok := raw.(map[string]any); ok && app["name"] == values["CASDOOR_APPLICATION"] {
					values["CASDOOR_CLIENT_SECRET"], _ = app["clientSecret"].(string)
				}
			}
		}
		if values["CASDOOR_CLIENT_SECRET"] == "" {
			values["CASDOOR_CLIENT_SECRET"] = rand.Text() + rand.Text()
		}
	}
	if len(values["CASDOOR_CLIENT_SECRET"]) < 24 {
		return fmt.Errorf("CASDOOR_CLIENT_SECRET must contain at least 24 characters")
	}
	if values["CASDOOR_ADMIN_PASSWORD"] == "" {
		values["CASDOOR_ADMIN_PASSWORD"] = rand.Text()
	}
	if values["CASDOOR_DEMO_PASSWORD"] == "" {
		values["CASDOOR_DEMO_PASSWORD"] = rand.Text()
	}
	if values["CASDOOR_BUILTIN_SECRET"] == "" {
		values["CASDOOR_BUILTIN_SECRET"] = rand.Text() + rand.Text()
	}
	values["AUTH_ENABLED"] = "true"
	values["CASDOOR_UID"] = strconv.Itoa(os.Getuid())
	values["CASDOOR_GID"] = strconv.Itoa(os.Getgid())
	if _, ok := seed["certs"]; !ok {
		builtin, err := certificate("cert-built-in")
		if err != nil {
			return err
		}
		project, err := certificate("pvzce-token")
		if err != nil {
			return err
		}
		seed["certs"] = []any{builtin, project}
	}
	seed["organizations"] = []any{map[string]any{"owner": "admin", "name": "built-in", "hasPrivilegeConsent": true}, map[string]any{
		"owner": "admin", "name": "pvzce", "displayName": "PvZ CE", "passwordType": "bcrypt",
		"passwordOptions": []string{"AtLeast8"}, "languages": []string{"zh", "en"}, "countryCodes": []string{"CN", "PH"},
		"defaultApplication": values["CASDOOR_APPLICATION"], "defaultTokenFormat": "JWT-Standard", "isProfilePublic": false,
		"accountItems": []any{map[string]any{"name": "Name", "visible": true, "viewRule": "Self", "modifyRule": "Admin"}, map[string]any{"name": "Display name", "visible": true, "viewRule": "Self", "modifyRule": "Self"}, map[string]any{"name": "Password", "visible": true, "viewRule": "Self", "modifyRule": "Self"}, map[string]any{"name": "Email", "visible": true, "viewRule": "Self", "modifyRule": "Self"}},
	}}
	application := func(name, organization, clientID, secret, cert, home string, signup bool) map[string]any {
		return map[string]any{
			"owner": "admin", "name": name, "displayName": "PvZ CE", "organization": organization, "cert": cert,
			"homepageUrl": home, "enablePassword": true, "enableSignUp": signup, "clientId": clientID, "clientSecret": secret,
			"grantTypes": []string{"authorization_code"}, "redirectUris": []string{home + "/auth/callback"}, "tokenFormat": "JWT-Standard", "expireInHours": 1,
			"signinMethods":     []any{map[string]any{"name": "Password", "displayName": "密码", "rule": "All"}},
			"signupItems":       []any{map[string]any{"name": "ID", "visible": false, "required": true, "rule": "Random"}, map[string]any{"name": "Username", "visible": true, "required": true, "rule": "None"}, map[string]any{"name": "Display name", "visible": true, "required": true, "rule": "None"}, map[string]any{"name": "Password", "visible": true, "required": true, "rule": "None"}, map[string]any{"name": "Confirm password", "visible": true, "required": true, "rule": "None"}, map[string]any{"name": "Email", "visible": true, "required": false, "rule": "None"}},
			"failedSigninLimit": 5, "failedSigninFrozenTime": 15,
		}
	}
	seed["applications"] = []any{
		application("app-built-in", "built-in", "pvzce-management", values["CASDOOR_BUILTIN_SECRET"], "cert-built-in", values["CASDOOR_ISSUER_URL"], false),
		application(values["CASDOOR_APPLICATION"], "pvzce", values["CASDOOR_CLIENT_ID"], values["CASDOOR_CLIENT_SECRET"], "pvzce-token", values["APP_PUBLIC_URL"], true),
	}
	seed["users"] = []any{
		map[string]any{"owner": "built-in", "name": "admin", "isForbidden": true},
		map[string]any{"owner": "built-in", "name": "pvzce-admin", "type": "normal-user", "displayName": "PvZ CE Administrator", "password": values["CASDOOR_ADMIN_PASSWORD"], "isAdmin": true, "signupApplication": "app-built-in"},
		map[string]any{"owner": "pvzce", "name": "demo", "type": "normal-user", "displayName": "本地测试账户", "password": values["CASDOOR_DEMO_PASSWORD"], "isAdmin": false, "email": "demo@example.invalid", "signupApplication": values["CASDOOR_APPLICATION"]},
	}
	encoded, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := writePrivate(seedPath, append(encoded, '\n')); err != nil {
		return err
	}
	dsn := "user=" + quotePostgres(values["DB_USER"]) + " password=" + quotePostgres(values["DB_PASSWORD"]) + " host=database port=5432 sslmode=disable dbname=casdoor"
	configuration := fmt.Sprintf("appname = casdoor\nhttpport = 8000\nrunmode = prod\ncopyrequestbody = true\ndriverName = postgres\ndataSourceName = %s\ndbName = casdoor\nshowSql = false\norigin = %s\noriginFrontend = %s\nstaticBaseUrl = %s\nisDemoMode = false\ndefaultApplication = app-built-in\ninitDataFile = /conf/init_data.json\ninitDataMerge = true\ninitDataNewOnly = false\nlogConfig = {\"adapter\":\"console\"}\nfrontendBaseDir = /web\n", dsn, values["CASDOOR_ISSUER_URL"], values["CASDOOR_ISSUER_URL"], values["CASDOOR_ISSUER_URL"])
	if err := writePrivate(filepath.Join(directory, "app.conf"), []byte(configuration)); err != nil {
		return err
	}
	credentials := fmt.Sprintf("本地 Casdoor 初始账户（用户改密后，此文件不会自动更新）\n\n本站登录: %s/auth/login\n组织: pvzce\n用户名: demo\n初始密码: %s\n\nCasdoor 管理: %s\n组织: built-in\n用户名: pvzce-admin\n初始密码: %s\n", values["APP_PUBLIC_URL"], values["CASDOOR_DEMO_PASSWORD"], values["CASDOOR_ISSUER_URL"], values["CASDOOR_ADMIN_PASSWORD"])
	if err := writePrivate(filepath.Join(directory, "credentials.txt"), []byte(credentials)); err != nil {
		return err
	}
	// Update only managed keys while retaining all existing configuration and comments.
	managed := []string{"DB_PASSWORD", "AUTH_ENABLED", "APP_PUBLIC_URL", "CASDOOR_ISSUER_URL", "CASDOOR_APPLICATION", "CASDOOR_CLIENT_ID", "CASDOOR_CLIENT_SECRET", "CASDOOR_ADMIN_PASSWORD", "CASDOOR_DEMO_PASSWORD", "CASDOOR_BUILTIN_SECRET", "CASDOOR_UID", "CASDOOR_GID", "AUTH_SESSION_TTL"}
	for _, key := range managed {
		found := false
		for i, line := range lines {
			if strings.HasPrefix(line, key+"=") {
				// Preserve the user's quoting of unchanged values, including literal $ and backticks.
				if original[key] != values[key] {
					lines[i] = key + "=" + strconv.Quote(values[key])
				}
				found = true
				break
			}
		}
		if !found {
			lines = append(lines, key+"="+strconv.Quote(values[key]))
		}
	}
	return writePrivate(envPath, []byte(strings.Join(lines, "\n")+"\n"))
}

func parseEnvironment(body string) (map[string]string, []string) {
	values := map[string]string{}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if decoded, err := strconv.Unquote(value); err == nil {
			value = decoded
		} else if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(key)] = value
	}
	return values, lines
}

func quotePostgres(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "'", "\\'") + "'"
}

func writePrivate(path string, body []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".bootstrap-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func certificate(name string) (map[string]any, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "PvZ CE " + name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"owner": "admin", "name": name, "displayName": name, "scope": "JWT", "type": "x509", "cryptoAlgorithm": "RS256", "bitSize": 2048, "expireInYears": 10,
		"certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "privateKey": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}, nil
}
