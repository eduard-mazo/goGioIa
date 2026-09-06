package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	// BOM del Bloc de notas + CRLF de Windows: el caso real de un archivo
	// editado en el equipo de destino.
	in := "\ufeffORACLE_HOST=10.0.0.5\r\n" +
		"# comentario\r\n" +
		"\r\n" +
		"export ORACLE_USER = useria \r\n" +
		"ORACLE_PASSWORD='a#b '\r\n" +
		"EMBED_KEEP_ALIVE=\"10m\"\r\n" +
		"ORACLE_PORT=1521  # listener\r\n" +
		"BANNER=\"linea1\\nlinea2\"\r\n" +
		"sin_igual\r\n" +
		"=valor_sin_clave\r\n"

	got, err := parseEnvFile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}
	want := map[string]string{
		"ORACLE_HOST":      "10.0.0.5",
		"ORACLE_USER":      "useria",
		"ORACLE_PASSWORD":  "a#b ", // comillas simples: literal, espacio incluido
		"EMBED_KEEP_ALIVE": "10m",
		"ORACLE_PORT":      "1521",
		"BANNER":           "linea1\nlinea2",
	}
	if len(got) != len(want) {
		t.Errorf("claves = %v, se esperaban %d", got, len(want))
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, se esperaba %q", k, got[k], w)
		}
	}
}

func TestLoadFromPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gogioia.env")
	body := "ORACLE_HOST=desde-archivo\nORACLE_USER=desde-archivo\nRAG_TOP_K=9\nEMBED_BATCH=cero\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORACLE_HOST", "desde-entorno")

	cfg := LoadFrom(path)

	if cfg.OracleHost != "desde-entorno" {
		t.Errorf("OracleHost = %q, el entorno debe ganar al archivo", cfg.OracleHost)
	}
	if cfg.Source("ORACLE_HOST") != "environment" {
		t.Errorf("procedencia ORACLE_HOST = %q", cfg.Source("ORACLE_HOST"))
	}
	if cfg.OracleUser != "desde-archivo" {
		t.Errorf("OracleUser = %q, el archivo debe ganar al default", cfg.OracleUser)
	}
	if cfg.Source("ORACLE_USER") != "file" {
		t.Errorf("procedencia ORACLE_USER = %q", cfg.Source("ORACLE_USER"))
	}
	if cfg.RAGTopK != 9 {
		t.Errorf("RAGTopK = %d, se esperaba 9", cfg.RAGTopK)
	}
	// Un entero inválido cae al default, igual que desde el entorno.
	if cfg.EmbedBatchSize != defaultEmbedBatch || cfg.Source("EMBED_BATCH") != "default" {
		t.Errorf("EMBED_BATCH = %d (%s), se esperaba el default",
			cfg.EmbedBatchSize, cfg.Source("EMBED_BATCH"))
	}
	if cfg.OracleSID != defaultOracleSID || cfg.Source("ORACLE_SID") != "default" {
		t.Errorf("ORACLE_SID = %q (%s), se esperaba el default",
			cfg.OracleSID, cfg.Source("ORACLE_SID"))
	}
	if cfg.ConfigFile != path {
		t.Errorf("ConfigFile = %q, se esperaba %q", cfg.ConfigFile, path)
	}
	if cfg.ConfigFileError != "" {
		t.Errorf("ConfigFileError = %q", cfg.ConfigFileError)
	}
}

func TestLoadFromMissingFile(t *testing.T) {
	// Una ruta inexistente no aborta Load: se reporta y main.go decide.
	cfg := LoadFrom(filepath.Join(t.TempDir(), "no-existe.env"))
	if cfg.ConfigFileError == "" {
		t.Error("se esperaba ConfigFileError con una ruta inexistente")
	}
	if cfg.ConfigFile != "" {
		t.Errorf("ConfigFile = %q, se esperaba vacío", cfg.ConfigFile)
	}
	if cfg.OracleSID != defaultOracleSID {
		t.Errorf("los defaults deben seguir aplicándose, OracleSID = %q", cfg.OracleSID)
	}
}

func TestFindConfigFileEnvVarWins(t *testing.T) {
	t.Setenv(EnvFileVar, "/ruta/explicita.env")
	if got := findConfigFile(); got != "/ruta/explicita.env" {
		t.Errorf("findConfigFile() = %q", got)
	}
}

func TestFindConfigFileWorkingDir(t *testing.T) {
	t.Setenv(EnvFileVar, "")
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("WEB_PORT=9999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got := findConfigFile()
	// En macOS el temp dir tiene enlaces simbólicos; se compara resuelto.
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		got = resolved
	}
	if want, err := filepath.EvalSymlinks(path); err == nil && got != want {
		t.Errorf("findConfigFile() = %q, se esperaba %q", got, want)
	}
}
