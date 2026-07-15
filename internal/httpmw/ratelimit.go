package httpmw

import (
	"sync"
	"time"
)

// RateLimiter es un token-bucket en memoria por clave (típicamente la IP del
// cliente). Sirve para frenar fuerza bruta en el login sin infraestructura
// adicional; al ser en memoria, el estado se pierde al reiniciar el proceso,
// lo cual es aceptable para este uso.
type RateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens repuestos por segundo
	burst   float64 // capacidad máxima del bucket
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter crea un limitador que repone ratePerSec tokens por segundo
// con capacidad burst. Ej.: NewRateLimiter(10.0/60, 10) → 10 intentos/minuto.
func NewRateLimiter(ratePerSec, burst float64) *RateLimiter {
	return &RateLimiter{
		rate:    ratePerSec,
		burst:   burst,
		buckets: make(map[string]*bucket),
		lastGC:  time.Now(),
	}
}

// Allow consume un token de la clave dada; devuelve false si el bucket está
// vacío (petición a rechazar con 429).
func (rl *RateLimiter) Allow(key string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Limpieza perezosa de buckets ya llenos (inactivos) cada 10 minutos.
	if now.Sub(rl.lastGC) > 10*time.Minute {
		for k, b := range rl.buckets {
			if b.tokens+now.Sub(b.last).Seconds()*rl.rate >= rl.burst {
				delete(rl.buckets, k)
			}
		}
		rl.lastGC = now
	}

	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
