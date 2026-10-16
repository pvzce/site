package casdoorbootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInitializationPreservesCredentialsAndSigningKeys(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(root, ".env")
	directory := filepath.Join(root, "configs", "casdoor")
	if err := os.WriteFile(envFile, []byte("DB_PASSWORD=existing-password\nPORT=8080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(envFile, directory); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(directory, "init_data.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(envFile, directory); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(directory, "init_data.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("bootstrap rotated signing keys or credentials on repeated execution")
	}
	updated, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	values, _ := parseEnvironment(string(updated))
	if values["DB_PASSWORD"] != "existing-password" || values["AUTH_ENABLED"] != "true" || len(values["CASDOOR_CLIENT_SECRET"]) < 24 {
		t.Fatal("existing configuration was not preserved")
	}
	var seed map[string]any
	if err := json.Unmarshal(second, &seed); err != nil {
		t.Fatal(err)
	}
	if len(seed["certs"].([]any)) != 2 {
		t.Fatal("fresh application and management signing certificates are required")
	}
	for _, path := range []string{envFile, filepath.Join(directory, "app.conf"), filepath.Join(directory, "init_data.json"), filepath.Join(directory, "credentials.txt")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("private configuration mode: %o", info.Mode().Perm())
		}
	}
}

func TestLiteralCredentialQuotingIsPreserved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	original := "DB_PASSWORD='keep$literal`credentials'\nCASDOOR_CLIENT_SECRET='existing$client`secret-long-enough'\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(path, filepath.Join(root, "casdoor")); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(updated, []byte(original)) {
		t.Fatal("literal credential quoting was rewritten")
	}
}
