package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

const minimalConfig = `
canarium:
  mode: disarmed
clients:
  - name: nas
    transport: ssh
    address: 10.0.0.1
plans:
  - name: outage
    trigger:
      condition: "true"
    shutdown:
      stages:
        - name: s
          when:
            condition: "true"
          clients: [nas]
    wake:
      gate:
        condition: "true"
`

// TestUnknownFieldIsRejected is the regression test for silent typos.
// Without KnownFields, `point_of_no_retrun: true` produced a stage with no
// point of no return and no complaint.
func TestUnknownFieldIsRejected(t *testing.T) {
	body := strings.Replace(minimalConfig,
		"          clients: [nas]",
		"          clients: [nas]\n          point_of_no_retrun: true", 1)

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a misspelled field was accepted silently")
	}
	if !strings.Contains(err.Error(), "point_of_no_retrun") {
		t.Errorf("error does not name the offending field: %v", err)
	}
}

func TestUnknownTopLevelKeyIsRejected(t *testing.T) {
	body := minimalConfig + "\nnot_a_real_section:\n  x: 1\n"

	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Error("an unknown top-level key was accepted")
	}
}

func TestValidConfigLoads(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Clients) != 1 || cfg.Clients[0].Name != "nas" {
		t.Errorf("unexpected clients: %+v", cfg.Clients)
	}
}

func TestEmptyConfigIsRejected(t *testing.T) {
	if _, err := Load(writeConfig(t, "")); err == nil {
		t.Error("an empty config file was accepted")
	}
}

func TestMultipleDocumentsAreRejected(t *testing.T) {
	body := minimalConfig + "\n---\ncanarium:\n  mode: armed\n"

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("a multi-document config was accepted; the second document " +
			"would have been silently ignored")
	}
	if !strings.Contains(err.Error(), "document") {
		t.Errorf("error does not explain the problem: %v", err)
	}
}

// TestMissingEnvIsAnErrorForTheDaemon is the regression test for unresolved
// ${VAR} references being left as literals. A credential silently became the
// string "${TOKEN}" and every request using it failed to authenticate with
// no indication why.
func TestMissingEnvIsAnErrorForTheDaemon(t *testing.T) {
	body := strings.Replace(minimalConfig,
		"    address: 10.0.0.1",
		"    address: 10.0.0.1\n    credentials: ${CANARIUM_TEST_UNSET_VAR}", 1)

	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("an unset environment variable was accepted")
	}
	if !strings.Contains(err.Error(), "CANARIUM_TEST_UNSET_VAR") {
		t.Errorf("error does not name the variable: %v", err)
	}
}

// TestMissingEnvIsAllowedForValidation: checking a config's structure in CI
// must not require production secrets.
func TestMissingEnvIsAllowedForValidation(t *testing.T) {
	body := strings.Replace(minimalConfig,
		"    address: 10.0.0.1",
		"    address: 10.0.0.1\n    credentials: ${CANARIUM_TEST_UNSET_VAR}", 1)

	res, err := LoadWith(writeConfig(t, body), LoadOptions{AllowMissingEnv: true})
	if err != nil {
		t.Fatalf("LoadWith: %v", err)
	}
	if len(res.MissingEnv) != 1 || res.MissingEnv[0] != "CANARIUM_TEST_UNSET_VAR" {
		t.Errorf("MissingEnv = %v, want [CANARIUM_TEST_UNSET_VAR]", res.MissingEnv)
	}
	if res.Config.Clients[0].Credentials == "" {
		t.Error("no placeholder was substituted; the rest of the file could not be checked")
	}
}

func TestEnvExpansion(t *testing.T) {
	t.Setenv("CANARIUM_TEST_TOKEN", "s3cret-value")

	body := strings.Replace(minimalConfig,
		"    address: 10.0.0.1",
		"    address: 10.0.0.1\n    credentials: ${CANARIUM_TEST_TOKEN}", 1)

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Clients[0].Credentials; got != "s3cret-value" {
		t.Errorf("credentials = %q, want s3cret-value", got)
	}
}

// TestEnvExpansionCannotInjectYAML: substitution happens on raw text before
// parsing, so a value containing YAML syntax could otherwise restructure the
// document.
func TestEnvExpansionCannotInjectYAML(t *testing.T) {
	// A value that would open a new mapping key if spliced in raw.
	t.Setenv("CANARIUM_TEST_TOKEN", "abc\nmode: armed\nx: ")

	body := strings.Replace(minimalConfig,
		"    address: 10.0.0.1",
		"    address: 10.0.0.1\n    credentials: ${CANARIUM_TEST_TOKEN}", 1)

	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		// Rejecting it outright is also an acceptable outcome.
		return
	}

	if cfg.Canarium.Mode == "armed" {
		t.Fatal("an environment variable's value altered the config structure " +
			"and armed the executor")
	}
	if !strings.Contains(cfg.Clients[0].Credentials, "mode: armed") {
		t.Errorf("credentials = %q, want the literal value preserved",
			cfg.Clients[0].Credentials)
	}
}

func TestEnvExpansionHandlesSpecialCharacters(t *testing.T) {
	for _, value := range []string{
		"plain",
		"with:colon",
		"with #hash",
		"with'quote",
		`with"doublequote`,
		"with{brace}",
		"with[bracket]",
		"*anchor",
		"@at",
		"%percent",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CANARIUM_TEST_TOKEN", value)

			body := strings.Replace(minimalConfig,
				"    address: 10.0.0.1",
				"    address: 10.0.0.1\n    credentials: ${CANARIUM_TEST_TOKEN}", 1)

			cfg, err := Load(writeConfig(t, body))
			if err != nil {
				t.Fatalf("Load with credential %q: %v", value, err)
			}
			if got := cfg.Clients[0].Credentials; got != value {
				t.Errorf("credentials = %q, want %q", got, value)
			}
		})
	}
}
