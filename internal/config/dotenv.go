package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Nombres de archivo buscados cuando no se indica una ruta explícita. Se
// prueban primero junto al ejecutable (el caso de despliegue: un .exe y su
// archivo de configuración en la misma carpeta) y después en el directorio
// de trabajo (el caso de desarrollo: `go run ./cmd/server` desde la raíz).
var configFileNames = []string{"gogioia.env", ".env"}

// EnvFileVar permite fijar la ruta del archivo de configuración por entorno,
// útil cuando el ejecutable se lanza como servicio y no se pueden pasar flags.
const EnvFileVar = "GOGIOIA_CONFIG"

// findConfigFile devuelve la primera ruta candidata que existe, o "" si no hay
// ninguna. No es un error: el archivo es opcional y todo sigue resolviéndose
// por entorno y defaults.
func findConfigFile() string {
	if p := os.Getenv(EnvFileVar); p != "" {
		return p // ruta explícita: se devuelve exista o no, para poder avisar.
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	for _, dir := range dirs {
		for _, name := range configFileNames {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// readEnvFile parsea un archivo de pares clave=valor. Devuelve un mapa vacío
// (no nil) y error si el archivo no se puede leer.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return map[string]string{}, err
	}
	defer f.Close()
	return parseEnvFile(f)
}

// parseEnvFile acepta el formato habitual de los archivos .env:
//
//	# comentario
//	CLAVE=valor
//	export CLAVE=valor          → el prefijo `export` se ignora
//	CLAVE="valor con espacios"  → comillas dobles: se expanden \n \t \r \\ \"
//	CLAVE='literal'             → comillas simples: sin expansión
//	CLAVE=valor  # comentario   → comentario al final (solo si va sin comillas)
//
// Tolera CRLF y el BOM que añade el Bloc de notas de Windows al guardar en
// UTF-8. Las líneas mal formadas se ignoran en vez de abortar el arranque.
func parseEnvFile(r io.Reader) (map[string]string, error) {
	values := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimSpace(rest)
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = unquoteEnvValue(strings.TrimSpace(raw))
	}
	if err := sc.Err(); err != nil {
		return values, fmt.Errorf("leyendo configuración: %w", err)
	}
	return values, nil
}

// unquoteEnvValue quita las comillas envolventes y, solo en el caso de las
// comillas dobles, expande los escapes. Sin comillas se recorta un comentario
// final, de modo que `PASSWORD=a#b` conserva la almohadilla pero
// `PORT=1521  # listener` no.
func unquoteEnvValue(v string) string {
	if len(v) >= 2 {
		switch q := v[0]; {
		case q == '"' && v[len(v)-1] == '"':
			return expandEscapes(v[1 : len(v)-1])
		case q == '\'' && v[len(v)-1] == '\'':
			return v[1 : len(v)-1]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

func expandEscapes(v string) string {
	return strings.NewReplacer(
		`\n`, "\n",
		`\r`, "\r",
		`\t`, "\t",
		`\"`, `"`,
		`\\`, `\`,
	).Replace(v)
}
