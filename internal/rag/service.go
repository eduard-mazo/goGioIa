// Package rag implementa el pipeline de Retrieval-Augmented Generation:
// ingesta de PDFs (extracción → chunking → embeddings → Oracle 23ai) y
// preparación de respuestas (retrieval por similitud coseno + prompt para el
// LLM). El «entrenamiento» del asistente es exactamente esta ingesta: cada
// documento subido pasa a formar parte de la base de conocimiento vectorial.
package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"goGioIa/internal/config"
	"goGioIa/internal/ollama"
	"goGioIa/internal/store"
)

// nomic-embed-text rinde mejor con estos prefijos de tarea.
const (
	docPrefix   = "search_document: "
	queryPrefix = "search_query: "
)

// maxContextChunkChars recorta cada chunk citado en el prompt.
const maxContextChunkChars = 2000

// maxAttachChars limita el total de texto adjunto inyectado en el prompt.
const maxAttachChars = 16000

// Service orquesta ingesta y consultas RAG.
type Service struct {
	cfg    config.Config
	store  *store.Store
	ollama *ollama.Client
	// wake despierta a los workers de la cola cuando entra trabajo nuevo
	// (buffered: si nadie escucha, el sondeo periódico lo recoge igual).
	wake chan struct{}
}

// New construye el servicio RAG. Llamar a Start para arrancar los workers.
func New(cfg config.Config, st *store.Store, ol *ollama.Client) *Service {
	return &Service{cfg: cfg, store: st, ollama: ol, wake: make(chan struct{}, 1)}
}

// Store expone el vector store (listar/eliminar documentos desde la API).
func (s *Service) Store() *store.Store { return s.store }

// ── Ingesta / entrenamiento ──────────────────────────────────────────────

// ErrDuplicate señala que el PDF (mismo SHA-256) ya está en la base.
type ErrDuplicate struct{ Doc *store.Document }

func (e *ErrDuplicate) Error() string {
	return fmt.Sprintf("el documento ya existe (%s, estado %s)", e.Doc.FileName, e.Doc.Status)
}

// IngestAsync registra el documento y lo encola: los workers (Start) lo
// procesan en segundo plano y el frontend sigue el avance consultando el
// estado. El archivo queda en document_files hasta completar la ingesta, así
// que un reinicio del servidor no la pierde. Si un intento anterior del mismo
// archivo quedó en FAILED, se elimina y se reintenta.
func (s *Service) IngestAsync(ctx context.Context, fileName string, data []byte, uploadedBy string) (string, error) {
	if !SupportedFile(fileName) {
		return "", fmt.Errorf("tipo de archivo no admitido: %s", SupportedTypesMsg)
	}
	if err := s.store.EnsureReady(ctx); err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	existing, err := s.store.FindDocumentByHash(ctx, hash)
	if err != nil {
		return "", fmt.Errorf("verificar duplicados: %w", err)
	}
	if existing != nil {
		if existing.Status != store.StatusFailed {
			return "", &ErrDuplicate{Doc: existing}
		}
		log.Printf("rag: reintento de %q: se descarta el intento fallido anterior", fileName)
		raw, err := store.ParseID(existing.ID)
		if err == nil {
			if err := s.store.DeleteDocument(ctx, raw); err != nil {
				return "", fmt.Errorf("limpiar intento fallido anterior: %w", err)
			}
		}
	}

	// Tope propio: guardar el archivo son cientos de inserciones pequeñas; si
	// Oracle se atasca, mejor un error visible que una subida colgada sin fin.
	stageCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	docID, err := s.store.CreateDocument(stageCtx, fileName, hash, MimeFor(fileName), uploadedBy, data)
	if err != nil {
		return "", fmt.Errorf("registrar documento: %w", err)
	}
	log.Printf("rag: %q (%d bytes) en cola de ingesta", fileName, len(data))

	s.wakeWorkers()

	return hex.EncodeToString(docID), nil
}

// wakeWorkers avisa a la cola sin bloquear (el sondeo periódico es la red).
func (s *Service) wakeWorkers() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// runIngest ejecuta el pipeline completo sobre un documento ya registrado:
// UPLOADED → EXTRACTING → CHUNKED → EMBEDDED (o FAILED con el motivo, que
// también se devuelve para registrarlo en el trabajo de la cola).
func (s *Service) runIngest(docID []byte, fileName string, data []byte) error {
	// Independiente de la petición HTTP: la ingesta sobrevive al upload.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	fail := func(stage string, err error) error {
		log.Printf("rag: ingesta de %q falló en %s: %v", fileName, stage, err)
		msg := fmt.Sprintf("%s: %v", stage, err)
		if len(msg) > 3900 {
			msg = msg[:3900]
		}
		if err := s.store.SetDocumentStatus(ctx, docID, store.StatusFailed, msg); err != nil {
			log.Printf("rag: no se pudo marcar FAILED: %v", err)
		}
		return fmt.Errorf("%s: %w", stage, err)
	}

	// 0. Reintentos: descartar restos de un intento anterior (páginas/chunks
	// parciales violarían las restricciones de unicidad).
	if err := s.store.ResetDocumentContent(ctx, docID); err != nil {
		return fail("limpiar contenido previo", err)
	}
	started := time.Now()

	// 1. Extracción de texto (los PDF por página, para citar fuentes; los
	// archivos de texto como una sola página sin numerar).
	if err := s.store.SetDocumentStatus(ctx, docID, store.StatusExtracting, ""); err != nil {
		return fail("actualizar estado", err)
	}
	pages, err := extractPages(fileName, data)
	if err != nil {
		return fail("extracción de texto", err)
	}
	for _, p := range pages {
		if p.Text == "" {
			continue
		}
		if err := s.store.InsertPage(ctx, docID, p.Number, p.Text); err != nil {
			return fail("guardar páginas", err)
		}
	}

	// 2. Chunking.
	chunks := chunkPages(pages, s.cfg.ChunkSize, s.cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return fail("chunking", fmt.Errorf("el documento no produjo fragmentos de texto"))
	}
	if s.cfg.RAGDebug {
		for _, c := range chunks {
			log.Printf("rag[debug]: %q chunk %d (pág %d, %d chars): %s",
				fileName, c.Index, c.Page, len(c.Text), preview(c.Text, 80))
		}
	}
	if err := s.store.SetDocumentStatus(ctx, docID, store.StatusChunked, ""); err != nil {
		return fail("actualizar estado", err)
	}

	// 3. Embeddings por lotes + inserción en el vector store.
	embedStart := time.Now()
	batchSize := s.cfg.EmbedBatch
	for from := 0; from < len(chunks); from += batchSize {
		batch := chunks[from:min(from+batchSize, len(chunks))]
		inputs := make([]string, len(batch))
		for i, c := range batch {
			inputs[i] = docPrefix + c.Text
		}
		vectors, err := s.embedBatch(ctx, inputs)
		if err != nil {
			return fail("embeddings", fmt.Errorf("chunks %d-%d de %d: %w", from+1, from+len(batch), len(chunks), err))
		}
		for i, c := range batch {
			if err := s.store.InsertChunk(ctx, docID, c.Index, c.Page, c.Text,
				estimateTokens(c.Text), vectors[i], s.cfg.EmbedModel); err != nil {
				return fail("guardar chunks", err)
			}
		}
	}

	// 4. Listo: el documento forma parte de la base de conocimiento.
	// pageCount = mayor número de página (0 en archivos de texto sin paginar).
	pageCount := 0
	for _, p := range pages {
		pageCount = max(pageCount, p.Number)
	}
	if err := s.store.FinishDocument(ctx, docID, pageCount); err != nil {
		return fail("finalizar documento", err)
	}
	// La base cambió: invalida (por versión) la cache semántica de respuestas.
	if err := s.store.BumpKBVersion(ctx); err != nil {
		log.Printf("rag: no se pudo incrementar la versión de la base: %v", err)
	}
	totalChars := 0
	for _, c := range chunks {
		totalChars += len(c.Text)
	}
	log.Printf("rag: %q ingerido (%d páginas, %d chunks, %d chars) en %s [embeddings %s]",
		fileName, pageCount, len(chunks), totalChars,
		time.Since(started).Round(time.Second), time.Since(embedStart).Round(time.Second))
	return nil
}

// preview compacta un texto para el log: espacios colapsados y recorte.
func preview(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}

// embedBatch vectoriza un lote y, si el lote completo falla pese a los
// reintentos del cliente (p.ej. el runner de Ollama se reinició bajo carga),
// degrada a vectorizar de una en una para aislar el fallo en vez de perder
// toda la ingesta.
func (s *Service) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	vecs, err := s.ollama.Embed(ctx, s.cfg.EmbedModel, inputs)
	if err == nil || len(inputs) == 1 {
		return vecs, err
	}
	log.Printf("rag: lote de %d embeddings falló (%v); reintentando de uno en uno", len(inputs), err)
	out := make([][]float32, 0, len(inputs))
	for i, in := range inputs {
		vec, err := s.ollama.EmbedOne(ctx, s.cfg.EmbedModel, in)
		if err != nil {
			return nil, fmt.Errorf("entrada %d del lote: %w", i+1, err)
		}
		out = append(out, vec)
	}
	return out, nil
}

// ── Consulta (retrieval + prompt) ────────────────────────────────────────

// AttachedDoc es un documento adjuntado a la conversación con el clip (no
// forma parte de la base de conocimiento) que acompaña la pregunta y se
// inyecta en el contexto del prompt junto a las fuentes recuperadas.
type AttachedDoc struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Prepared contiene todo lo necesario para generar la respuesta en streaming
// y, al terminar, completar la trazabilidad en rag_queries.
type Prepared struct {
	QueryID  string               // hex, para feedback desde el frontend
	Sources  []store.SearchResult // chunks recuperados, ya ordenados
	Messages []ollama.Message     // prompt final para el LLM
	Model    string               // modelo con el que se generará
	Options  map[string]any       // parámetros del modelo

	// Cached indica que la respuesta salió de la cache semántica: no hay que
	// llamar al LLM, Answer y SourcesJSON traen lo que se debe emitir.
	Cached      bool
	Answer      string
	SourcesJSON string

	// cache trae la clave para poblar la cache al terminar (miss cacheable).
	cache *cacheSeed
}

// cacheSeed es la clave con la que se guardará la respuesta generada.
type cacheSeed struct {
	hash       string
	question   string
	vec        []float32
	model      string
	templateID []byte
	kbVersion  int64
}

// questionHash normaliza la pregunta (minúsculas, espacios colapsados) y la
// resume para el atajo de hit exacto de la cache.
func questionHash(question string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(question), " "))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// embedQueryCached vectoriza una consulta pasando por embedding_cache: el
// mismo texto no se envía dos veces a Ollama.
func (s *Service) embedQueryCached(ctx context.Context, question string) ([]float32, error) {
	sum := sha256.Sum256([]byte(s.cfg.EmbedModel + "\x00" + queryPrefix + question))
	hash := hex.EncodeToString(sum[:])
	if vec, err := s.store.CachedEmbedding(ctx, hash); err == nil && vec != nil {
		return vec, nil
	} else if err != nil {
		log.Printf("rag: cache de embeddings no disponible: %v", err)
	}
	vec, err := s.ollama.EmbedOne(ctx, s.cfg.EmbedModel, queryPrefix+question)
	if err != nil {
		return nil, err
	}
	if err := s.store.PutCachedEmbedding(ctx, hash, s.cfg.EmbedModel, vec); err != nil {
		log.Printf("rag: no se pudo cachear el embedding: %v", err)
	}
	return vec, nil
}

// PrepareAsk vectoriza la pregunta, recupera los chunks más afines desde
// Oracle 23ai, construye el prompt con la plantilla activa (incluyendo los
// documentos adjuntos de la conversación, si los hay) y deja registrada la
// consulta (rag_queries + rag_retrieved_chunks). history son turnos previos
// de la conversación: dan memoria de seguimiento («¿y en qué página está?»)
// y van como mensajes normales antes del prompt con el contexto.
func (s *Service) PrepareAsk(ctx context.Context, question, model, sessionID, userID string, attached []AttachedDoc, attachmentIDs []string, history []ollama.Message) (*Prepared, error) {
	if err := s.store.EnsureReady(ctx); err != nil {
		return nil, err
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, fmt.Errorf("la pregunta no puede estar vacía")
	}
	if model == "" {
		model = s.cfg.RAGModel
	}

	// Plantilla activa (también es parte de la clave de la cache).
	templateID, templateText, err := s.store.ActiveTemplate(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("cargar plantilla de prompt: %w", err)
	}

	// 0. Cache semántica. Consultar y sembrar tienen reglas distintas:
	// se SIEMBRA solo sin historial (la respuesta a una pregunta de
	// seguimiento depende del contexto y no es reutilizable), así la cache
	// solo contiene preguntas autocontenidas; por eso se puede CONSULTAR
	// aunque haya historial — repetir una pregunta a mitad de conversación
	// también resuelve al instante. Los adjuntos anulan ambas.
	lookupOK := s.cfg.RAGCache && len(attached) == 0 && len(attachmentIDs) == 0
	seedOK := lookupOK && len(history) == 0
	var kbVersion int64
	var qhash string
	if lookupOK {
		if kbVersion, err = s.store.KBVersion(ctx); err != nil {
			log.Printf("rag: sin versión de la base, cache desactivada: %v", err)
			lookupOK, seedOK = false, false
		}
	}
	if lookupOK {
		qhash = questionHash(question)
		if hit, err := s.store.LookupCacheExact(ctx, qhash, model, templateID, kbVersion); err != nil {
			log.Printf("rag: lookup exacto de cache falló: %v", err)
		} else if hit != nil {
			return s.preparedFromCache(ctx, hit, question, nil, model, sessionID, userID, templateID)
		}
	}

	// 1. Embedding de la consulta (mismo espacio vectorial que los chunks).
	embedStart := time.Now()
	qVec, err := s.embedQueryCached(ctx, question)
	if err != nil {
		return nil, fmt.Errorf("vectorizar la pregunta: %w", err)
	}
	embedDur := time.Since(embedStart)

	// 1b. Cache semántica por cercanía: una pregunta equivalente ya
	// respondida se sirve sin generar.
	if lookupOK {
		if hit, err := s.store.LookupCacheSemantic(ctx, qVec, model, templateID, kbVersion, 1-s.cfg.RAGCacheSim); err != nil {
			log.Printf("rag: lookup semántico de cache falló: %v", err)
		} else if hit != nil {
			return s.preparedFromCache(ctx, hit, question, qVec, model, sessionID, userID, templateID)
		}
	}

	// 2. Retrieval por similitud coseno.
	searchStart := time.Now()
	sources, err := s.store.SearchChunks(ctx, qVec, s.cfg.RAGTopK)
	if err != nil {
		return nil, fmt.Errorf("búsqueda vectorial: %w", err)
	}
	// El diagnóstico clave de una respuesta «sin contexto» es esta línea:
	// cuántas fuentes se recuperaron y con qué afinidad.
	if len(sources) == 0 {
		log.Printf("rag: consulta %q → 0 fuentes: la respuesta irá sin contexto de la base", preview(question, 60))
	} else {
		log.Printf("rag: consulta %q → %d fuentes (similitud %.2f–%.2f) [embed %s, búsqueda %s]",
			preview(question, 60), len(sources),
			sources[len(sources)-1].Similarity, sources[0].Similarity,
			embedDur.Round(time.Millisecond), time.Since(searchStart).Round(time.Millisecond))
	}
	if s.cfg.RAGDebug {
		for i, r := range sources {
			log.Printf("rag[debug]: fuente %d: %s pág %d sim %.3f: %s",
				i+1, r.FileName, r.PageNumber, r.Similarity, preview(r.Text, 80))
		}
	}

	// 2b. Anexos referenciados por id: pequeños completos, grandes vía
	// retrieval con el mismo embedding de la pregunta.
	attached = append(attached, s.resolveAttachments(ctx, attachmentIDs, func() []float32 { return qVec })...)

	// 3. Prompt desde la plantilla activa (versionada en prompt_templates).
	prompt := renderPrompt(templateText, question, sources, attached)

	// 4. Trazabilidad: consulta + chunks usados.
	queryID, err := s.store.CreateQuery(ctx, store.SessionID(sessionID), userID, question, qVec, model, templateID)
	if err != nil {
		return nil, fmt.Errorf("registrar consulta: %w", err)
	}
	if err := s.store.LogRetrievedChunks(ctx, queryID, sources); err != nil {
		log.Printf("rag: no se pudieron registrar los chunks recuperados: %v", err)
	}

	// Con adjuntos el prompt crece: se amplía la ventana de contexto para que
	// Ollama no los descarte silenciosamente.
	numCtx := 8192
	if len(attached) > 0 {
		numCtx = 16384
	}
	msgs := make([]ollama.Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	msgs = append(msgs, ollama.Message{Role: "user", Content: prompt})

	prep := &Prepared{
		QueryID:  hex.EncodeToString(queryID),
		Sources:  sources,
		Messages: msgs,
		Model:    model,
		Options:  map[string]any{"num_ctx": numCtx, "temperature": 0.2},
	}
	// Sin fuentes recuperadas la respuesta es un «no tengo información»:
	// cachearla congelaría ese fallo hasta el TTL o el próximo cambio de la
	// base; mejor regenerarla cada vez por si el retrieval se recupera.
	if seedOK && len(sources) > 0 {
		prep.cache = &cacheSeed{
			hash: qhash, question: question, vec: qVec,
			model: model, templateID: templateID, kbVersion: kbVersion,
		}
	}
	return prep, nil
}

// preparedFromCache registra la consulta (trazabilidad) y devuelve la
// respuesta cacheada lista para emitirse sin pasar por el LLM.
func (s *Service) preparedFromCache(ctx context.Context, hit *store.CachedAnswer, question string, qVec []float32, model, sessionID, userID string, templateID []byte) (*Prepared, error) {
	queryID, err := s.store.CreateQuery(ctx, store.SessionID(sessionID), userID, question, qVec, model, templateID)
	if err != nil {
		return nil, fmt.Errorf("registrar consulta: %w", err)
	}
	if err := s.store.SetQueryResponse(ctx, queryID, hit.Response); err != nil {
		log.Printf("rag: no se pudo guardar la respuesta cacheada en la consulta: %v", err)
	}
	if err := s.store.RecordCacheHit(ctx, hit.ID); err != nil {
		log.Printf("rag: no se pudo registrar el hit de cache: %v", err)
	}
	log.Printf("rag: respuesta servida desde la cache semántica (similitud %.3f)", hit.Similarity)
	return &Prepared{
		QueryID:     hex.EncodeToString(queryID),
		Model:       model,
		Cached:      true,
		Answer:      hit.Response,
		SourcesJSON: hit.SourcesJSON,
	}, nil
}

// FinishAsk completa la fila de rag_queries con la respuesta generada y, si
// la consulta era cacheable, la guarda en la cache semántica con las fuentes
// tal como se mostraron (sourcesJSON).
func (s *Service) FinishAsk(ctx context.Context, prep *Prepared, response, sourcesJSON string) error {
	id, err := store.ParseID(prep.QueryID)
	if err != nil {
		return err
	}
	if err := s.store.SetQueryResponse(ctx, id, response); err != nil {
		return err
	}
	if prep.cache != nil && strings.TrimSpace(response) != "" {
		c := prep.cache
		ttl := time.Duration(s.cfg.RAGCacheTTLHours) * time.Hour
		if err := s.store.PutCache(ctx, c.hash, c.question, c.vec, c.model,
			c.templateID, c.kbVersion, response, sourcesJSON, ttl); err != nil {
			log.Printf("rag: no se pudo poblar la cache semántica: %v", err)
		}
	}
	return nil
}

// Feedback guarda la valoración del usuario (-1 / 0 / 1) sobre una respuesta.
func (s *Service) Feedback(ctx context.Context, queryIDHex string, rating int, comment, createdBy string) error {
	if err := s.store.EnsureReady(ctx); err != nil {
		return err
	}
	id, err := store.ParseID(queryIDHex)
	if err != nil {
		return err
	}
	if rating < -1 || rating > 1 {
		return fmt.Errorf("rating fuera de rango (-1, 0, 1)")
	}
	if err := s.store.InsertFeedback(ctx, id, rating, comment, createdBy); err != nil {
		return err
	}
	// Un 👎 invalida las respuestas cacheadas equivalentes: el ciclo de
	// feedback tiene efecto inmediato en lo que se sirve.
	if rating < 0 {
		qhash := ""
		if text, err := s.store.QueryText(ctx, id); err == nil {
			qhash = questionHash(text)
		}
		if n, err := s.store.InvalidateCache(ctx, qhash, id, 1-s.cfg.RAGCacheSim); err != nil {
			log.Printf("rag: no se pudo invalidar la cache por feedback: %v", err)
		} else if n > 0 {
			log.Printf("rag: %d entrada(s) de cache invalidada(s) por feedback negativo", n)
		}
	}
	return nil
}

// renderPrompt sustituye {context} y {question} en la plantilla. El contexto
// reúne los chunks recuperados de Oracle y, a continuación, los documentos
// adjuntos de la conversación (recortados a un presupuesto total).
func renderPrompt(template, question string, sources []store.SearchResult, attached []AttachedDoc) string {
	var b strings.Builder
	if len(sources) == 0 {
		b.WriteString("(La base de conocimiento no devolvió resultados para esta pregunta.)\n\n")
	}
	for i, src := range sources {
		text := src.Text
		if len(text) > maxContextChunkChars {
			text = text[:maxContextChunkChars] + "…"
		}
		loc := src.FileName
		if src.PageNumber > 0 {
			loc = fmt.Sprintf("%s (pág. %d)", src.FileName, src.PageNumber)
		}
		fmt.Fprintf(&b, "[Fuente %d] %s\n%s\n\n", i+1, loc, text)
	}
	budget := maxAttachChars
	for i, doc := range attached {
		if budget <= 0 {
			break
		}
		text := strings.TrimSpace(doc.Text)
		if text == "" {
			continue
		}
		if len(text) > budget {
			text = text[:budget] + "… (recortado)"
		}
		budget -= len(text)
		fmt.Fprintf(&b, "[Adjunto %d] %s\n%s\n\n", i+1, doc.Name, text)
	}
	out := strings.ReplaceAll(template, "{context}", strings.TrimSpace(b.String()))
	out = strings.ReplaceAll(out, "{question}", question)
	return out
}
