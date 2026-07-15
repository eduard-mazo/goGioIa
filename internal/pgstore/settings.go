package pgstore

import (
	"context"
	"strconv"
	"time"
)

// settingsTTL es cuánto vive cada valor en la caché en memoria. Mantenerlo
// corto permite ajustar parámetros en caliente desde la BD sin reiniciar.
const settingsTTL = 30 * time.Second

type cachedSetting struct {
	val string
	exp time.Time
}

// Setting devuelve el valor crudo de app.app_settings con caché de 30 s.
// Si la clave no existe o la BD no responde, devuelve fallback.
func (s *Store) Setting(ctx context.Context, key, fallback string) string {
	s.settingsMu.Lock()
	if c, ok := s.settingsCache[key]; ok && time.Now().Before(c.exp) {
		s.settingsMu.Unlock()
		return c.val
	}
	s.settingsMu.Unlock()

	var val string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM app.app_settings WHERE key = $1`, key).Scan(&val)
	if err != nil {
		return fallback
	}
	s.settingsMu.Lock()
	s.settingsCache[key] = cachedSetting{val: val, exp: time.Now().Add(settingsTTL)}
	s.settingsMu.Unlock()
	return val
}

// SettingInt devuelve un setting entero (fallback si falta o no parsea).
func (s *Store) SettingInt(ctx context.Context, key string, fallback int) int {
	if v := s.Setting(ctx, key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// SettingBool devuelve un setting booleano (fallback si falta o no parsea).
func (s *Store) SettingBool(ctx context.Context, key string, fallback bool) bool {
	if v := s.Setting(ctx, key, ""); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// InvalidateSetting fuerza la relectura de una clave (la usará el panel de
// administración al escribir un valor).
func (s *Store) InvalidateSetting(key string) {
	s.settingsMu.Lock()
	delete(s.settingsCache, key)
	s.settingsMu.Unlock()
}
