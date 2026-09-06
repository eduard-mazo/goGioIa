package server

// Endpoints del dashboard de operaciones RAG (/api/rag/ops/*). Todos son de
// solo lectura, agregan en SQL del lado servidor (internal/store/ops.go) y
// siguen las convenciones del resto de la API: JSON, errores {error}, 503 si
// Oracle no está disponible. La app no tiene modelo de autenticación (consola
// interna en red cerrada); si se añade, estos handlers son el punto de corte.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"goGioIa/internal/rag"
	"goGioIa/internal/store"
)

// ── Salud (cacheada) ─────────────────────────────────────────────────────

// Estados posibles de cada componente.
const (
	healthHealthy     = "healthy"
	healthDegraded    = "degraded"
	healthUnavailable = "unavailable"
	healthUnknown     = "unknown"
)

// opsHealth es el semáforo de salud de todos los componentes.
type opsHealth struct {
	App          string `json:"app"`
	Oracle       string `json:"oracle"`
	OracleDetail string `json:"oracleDetail,omitempty"`
	VectorIndex  string `json:"vectorIndex"`
	VectorDetail string `json:"vectorDetail,omitempty"`
	Ollama       string `json:"ollama"`
	OllamaDetail string `json:"ollamaDetail,omitempty"`
	EmbedModel   string `json:"embedModel"`
	EmbedDetail  string `json:"embedDetail,omitempty"`
	GenModel     string `json:"genModel"`
	GenDetail    string `json:"genDetail,omitempty"`
	Ingestion    string `json:"ingestion"`
	QueueDepth   int    `json:"queueDepth"`
	StuckDocs    int    `json:"stuckDocs"`

	CheckedAt  time.Time `json:"checkedAt"`
	TTLSeconds int       `json:"ttlSeconds"`
	Cached     bool      `json:"cached"`
}

// healthCache evita sondear Oracle/Ollama (y sobre todo el modelo de
// embeddings) en cada refresh de la UI.
type healthCache struct {
	mu   sync.Mutex
	at   time.Time
	data *opsHealth
}

var opsHealthCache healthCache

// handleOpsHealth reporta la salud de cada componente. El resultado se
// cachea OPS_HEALTH_TTL segundos; el readiness del modelo de embeddings se
// deriva de la actividad reciente y solo se sondea (1 token) si no la hay.
func (s *Server) handleOpsHealth(w http.ResponseWriter, r *http.Request) {
	ttl := time.Duration(s.cfg.OpsHealthTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = time.Minute
	}

	opsHealthCache.mu.Lock()
	if opsHealthCache.data != nil && time.Since(opsHealthCache.at) < ttl {
		cached := *opsHealthCache.data
		cached.Cached = true
		opsHealthCache.mu.Unlock()
		writeJSON(w, http.StatusOK, cached)
		return
	}
	opsHealthCache.mu.Unlock()

	h := s.computeHealth(r.Context(), ttl)

	opsHealthCache.mu.Lock()
	opsHealthCache.data, opsHealthCache.at = h, time.Now()
	opsHealthCache.mu.Unlock()

	writeJSON(w, http.StatusOK, *h)
}

func (s *Server) computeHealth(ctx context.Context, ttl time.Duration) *opsHealth {
	h := &opsHealth{
		App:        healthHealthy,
		CheckedAt:  time.Now(),
		TTLSeconds: int(ttl.Seconds()),
	}

	// Oracle + índice vectorial + cola de ingesta.
	if err := s.store.EnsureReady(ctx); err != nil {
		h.Oracle, h.OracleDetail = healthUnavailable, err.Error()
		h.VectorIndex, h.Ingestion = healthUnknown, healthUnknown
	} else {
		h.Oracle = healthHealthy
		if present, err := s.store.VectorIndexPresent(ctx); err != nil {
			h.VectorIndex, h.VectorDetail = healthUnknown, err.Error()
		} else if present {
			h.VectorIndex = healthHealthy
		} else {
			h.VectorIndex = healthDegraded
			h.VectorDetail = "sin índice vectorial (¿vector_memory_size?); la búsqueda sigue funcionando en modo exacto"
		}
		if depth, stuck, err := s.store.IngestionQueue(ctx); err != nil {
			h.Ingestion = healthUnknown
		} else {
			h.QueueDepth, h.StuckDocs = depth, stuck
			h.Ingestion = healthHealthy
			if stuck > 0 {
				h.Ingestion = healthDegraded
			}
		}
	}

	// Ollama: alcanzable ≠ modelos listos; se evalúan por separado.
	installed := map[string]bool{}
	if err := s.ollama.Ping(ctx); err != nil {
		h.Ollama, h.OllamaDetail = healthUnavailable, err.Error()
		h.EmbedModel, h.GenModel = healthUnknown, healthUnknown
		return h
	}
	h.Ollama = healthHealthy
	if tags, err := s.ollama.Tags(ctx); err == nil {
		for _, m := range tags {
			installed[m.Name] = true
		}
	}

	h.EmbedModel, h.EmbedDetail = s.embedModelHealth(ctx, ttl, installed)
	h.GenModel, h.GenDetail = s.genModelHealth(ctx, installed)
	return h
}

// embedModelHealth deriva el readiness del modelo de embeddings de los
// eventos recientes; solo si no hay actividad hace una sonda mínima (1 token,
// cacheada por el TTL). Nunca se reporta healthy por un simple GET /.
func (s *Server) embedModelHealth(ctx context.Context, ttl time.Duration, installed map[string]bool) (string, string) {
	if s.store.Ready() {
		if ok, at, detail, err := s.store.LastEmbedStatus(ctx); err == nil && !at.IsZero() {
			if time.Since(at) < 5*ttl {
				if ok {
					return healthHealthy, "último embedding correcto " + at.Format("15:04:05")
				}
				return healthDegraded, "último embedding falló: " + detail
			}
		}
	}
	// Sin actividad reciente: sonda controlada de 1 token.
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.ollama.WarmEmbed(probeCtx, s.cfg.EmbedModel); err != nil {
		if modelMissing(installed, s.cfg.EmbedModel) {
			return healthUnavailable, "el modelo no aparece en /api/tags y la sonda falló: " + err.Error()
		}
		return healthDegraded, "sonda de embeddings falló: " + err.Error()
	}
	return healthHealthy, "sonda de embeddings correcta"
}

// genModelHealth no sondea el modelo de generación: en una GPU de 2 GiB una
// generación de prueba desalojaría el modelo de embeddings. Se deriva de la
// presencia en /api/tags y de la última generación registrada.
func (s *Server) genModelHealth(ctx context.Context, installed map[string]bool) (string, string) {
	if modelMissing(installed, s.cfg.RAGModel) {
		return healthUnavailable, "el modelo no aparece en /api/tags"
	}
	if s.store.Ready() {
		if ok, at, detail, err := s.store.LastGenerationStatus(ctx); err == nil && !at.IsZero() {
			if ok {
				return healthHealthy, "última generación correcta " + at.Format("02/01 15:04")
			}
			return healthDegraded, "última generación falló: " + detail
		}
	}
	return healthUnknown, "instalado; sin actividad registrada (no se sondea para no desalojar el modelo de embeddings)"
}

// modelMissing comprueba si un modelo (con o sin tag) falta en /api/tags.
func modelMissing(installed map[string]bool, name string) bool {
	if len(installed) == 0 {
		return false // /api/tags no respondió: no se puede afirmar la ausencia
	}
	if installed[name] {
		return false
	}
	for tag := range installed {
		if strings.HasPrefix(tag, name+":") || strings.HasPrefix(name, strings.TrimSuffix(tag, ":latest")) {
			return false
		}
	}
	return true
}

// ── Métricas agregadas ───────────────────────────────────────────────────

func (s *Server) handleOpsOverview(w http.ResponseWriter, r *http.Request) {
	if !s.opsReady(w, r) {
		return
	}
	ov, err := s.store.OpsOverview(r.Context(), queryInt(r, "hours", 24), queryFloat(r, "weak", 0.5))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (s *Server) handleOpsTimeseries(w http.ResponseWriter, r *http.Request) {
	if !s.opsReady(w, r) {
		return
	}
	ts, err := s.store.OpsTimeseries(r.Context(), queryInt(r, "hours", 24))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) handleOpsDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.opsReady(w, r) {
		return
	}
	size := queryInt(r, "size", 25)
	page, err := s.store.OpsDocuments(r.Context(), store.DocFilter{
		Status:      r.URL.Query().Get("status"),
		Query:       r.URL.Query().Get("q"),
		OnlyErrors:  queryBool(r, "errors"),
		OnlyMissing: queryBool(r, "missing"),
		Hours:       queryInt(r, "hours", 0),
		Sort:        r.URL.Query().Get("sort"),
		Desc:        r.URL.Query().Get("dir") != "asc",
		Offset:      queryInt(r, "page", 0) * size,
		Limit:       size,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleOpsDocumentDetail(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identificador inválido"})
		return
	}
	if !s.opsReady(w, r) {
		return
	}
	detail, err := s.store.OpsDocumentDetail(r.Context(), id, s.cfg.EmbedMaxTokens)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "documento no encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleOpsQueries(w http.ResponseWriter, r *http.Request) {
	if !s.opsReady(w, r) {
		return
	}
	size := queryInt(r, "size", 25)
	page, err := s.store.OpsQueries(r.Context(), store.QueryFilter{
		Hours:     queryInt(r, "hours", 0),
		Status:    r.URL.Query().Get("status"),
		Feedback:  r.URL.Query().Get("feedback"),
		NoResults: queryBool(r, "noResults"),
		WeakBelow: queryFloat(r, "weak", 0),
		Model:     r.URL.Query().Get("model"),
		Sort:      r.URL.Query().Get("sort"),
		Desc:      r.URL.Query().Get("dir") != "asc",
		Offset:    queryInt(r, "page", 0) * size,
		Limit:     size,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleOpsQueryTrace(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identificador inválido"})
		return
	}
	if !s.opsReady(w, r) {
		return
	}
	trace, err := s.store.OpsQueryTrace(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "consulta no encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, trace)
}

func (s *Server) handleOpsTokens(w http.ResponseWriter, r *http.Request) {
	if !s.opsReady(w, r) {
		return
	}
	report, err := s.store.OpsTokens(r.Context(), queryInt(r, "hours", 24), s.cfg.EmbedMaxTokens)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleOpsModels combina el estado en vivo del host Ollama (/api/tags,
// /api/ps) con las métricas por modelo derivadas de rag_events.
func (s *Server) handleOpsModels(w http.ResponseWriter, r *http.Request) {
	if !s.opsReady(w, r) {
		return
	}
	ctx := r.Context()
	resp := map[string]any{
		"endpoint":         s.cfg.OllamaAPI,
		"embedModel":       s.cfg.EmbedModel,
		"generationModel":  s.cfg.RAGModel,
		"chatModel":        s.cfg.ModelName,
		"vectorDimension":  768,
		"embedMaxTokens":   s.cfg.EmbedMaxTokens,
		"ragNumCtx":        s.cfg.RAGNumCtx,
		"embedConcurrency": s.cfg.EmbedConcurrency,
		"embedKeepAlive":   s.cfg.EmbedKeepAlive,
		"maxAttempts":      s.ollama.MaxAttempts(),
		"reachable":        false,
	}
	if err := s.ollama.Ping(ctx); err == nil {
		resp["reachable"] = true
		if tags, err := s.ollama.Tags(ctx); err == nil {
			resp["installed"] = tags
		}
		if running, err := s.ollama.ListRunning(ctx); err == nil {
			resp["running"] = running
		}
	}
	stats, err := s.store.OpsModelStats(ctx, queryInt(r, "hours", 24), s.ollama.MaxAttempts())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	resp["stats"] = stats
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleOpsIntegrity(w http.ResponseWriter, r *http.Request) {
	if !s.opsReady(w, r) {
		return
	}
	checks := s.store.OpsIntegrity(r.Context(), s.cfg.EmbedModel, s.cfg.EmbedMaxTokens)
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

// ── Configuración (solo lectura, con fuente, sin secretos) ───────────────

// configEntry es un valor de configuración con su procedencia.
type configEntry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"` // environment | file | flag | default | code
	Secret bool   `json:"secret,omitempty"`
}

// handleOpsConfig expone la configuración efectiva en solo lectura. La app no
// tiene un mecanismo seguro de edición de configuración en caliente, así que
// no se ofrece edición. Los secretos nunca salen (solo su procedencia).
func (s *Server) handleOpsConfig(w http.ResponseWriter, _ *http.Request) {
	itoa := strconv.Itoa
	src := s.cfg.Source
	entries := []configEntry{
		{Key: "OLLAMA_API", Value: s.cfg.OllamaAPI, Source: src("OLLAMA_API")},
		{Key: "MODEL_NAME", Value: s.cfg.ModelName, Source: src("MODEL_NAME")},
		{Key: "EMBED_MODEL", Value: s.cfg.EmbedModel, Source: src("EMBED_MODEL")},
		{Key: "EMBED_MAX_TOKENS", Value: itoa(s.cfg.EmbedMaxTokens), Source: src("EMBED_MAX_TOKENS")},
		{Key: "EMBED_BATCH", Value: itoa(s.cfg.EmbedBatchSize), Source: src("EMBED_BATCH")},
		{Key: "EMBED_CONCURRENCY", Value: itoa(s.cfg.EmbedConcurrency), Source: src("EMBED_CONCURRENCY")},
		{Key: "EMBED_KEEP_ALIVE", Value: s.cfg.EmbedKeepAlive, Source: src("EMBED_KEEP_ALIVE")},
		{Key: "RAG_MODEL", Value: s.cfg.RAGModel, Source: src("RAG_MODEL")},
		{Key: "RAG_NUM_CTX", Value: itoa(s.cfg.RAGNumCtx), Source: src("RAG_NUM_CTX")},
		{Key: "RAG_TOP_K", Value: itoa(s.cfg.RAGTopK), Source: src("RAG_TOP_K")},
		{Key: "RAG_CHUNK_SIZE", Value: itoa(s.cfg.ChunkSize), Source: src("RAG_CHUNK_SIZE")},
		{Key: "RAG_CHUNK_OVERLAP", Value: itoa(s.cfg.ChunkOverlap), Source: src("RAG_CHUNK_OVERLAP")},
		{Key: "OPS_HEALTH_TTL", Value: itoa(s.cfg.OpsHealthTTLSeconds), Source: src("OPS_HEALTH_TTL")},
		{Key: "ORACLE_HOST", Value: s.cfg.OracleHost, Source: src("ORACLE_HOST")},
		{Key: "ORACLE_PORT", Value: itoa(s.cfg.OraclePort), Source: src("ORACLE_PORT")},
		{Key: "ORACLE_SID", Value: s.cfg.OracleSID, Source: src("ORACLE_SID")},
		{Key: "ORACLE_USER", Value: s.cfg.OracleUser, Source: src("ORACLE_USER")},
		{Key: "ORACLE_PASSWORD", Value: "••••••••", Source: src("ORACLE_PASSWORD"), Secret: true},
		{Key: "WEB_PORT", Value: s.cfg.WebPort, Source: src("WEB_PORT")},
		// Valores fijados en código (no configurables por entorno).
		{Key: "CONFIG_FILE", Value: configFileLabel(s.cfg.ConfigFile), Source: "code"},
		{Key: "VECTOR_DIMENSION", Value: "768", Source: "code"},
		{Key: "DOC_PREFIX", Value: rag.DocPrefix, Source: "code"},
		{Key: "QUERY_PREFIX", Value: rag.QueryPrefix, Source: "code"},
		{Key: "EMBED_MAX_ATTEMPTS", Value: itoa(s.ollama.MaxAttempts()), Source: "code"},
		{Key: "PROMPT_TEMPLATE", Value: "rag-default (prompt_templates, versionada en BD)", Source: "code"},
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// configFileLabel describe el archivo de configuración aplicado; sin archivo,
// la configuración viene solo del entorno y los defaults.
func configFileLabel(path string) string {
	if path == "" {
		return "(ninguno)"
	}
	return path
}

// ── Utilidades ───────────────────────────────────────────────────────────

// opsReady garantiza el esquema antes de consultar; responde 503 si Oracle no
// está disponible (mismo contrato que el resto de la API RAG).
func (s *Server) opsReady(w http.ResponseWriter, r *http.Request) bool {
	if err := s.store.EnsureReady(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

func queryFloat(r *http.Request, key string, def float64) float64 {
	if v := r.URL.Query().Get(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	return def
}

func queryBool(r *http.Request, key string) bool {
	v := r.URL.Query().Get(key)
	return v == "1" || v == "true"
}
