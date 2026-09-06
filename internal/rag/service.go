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
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"goGioIa/internal/config"
	"goGioIa/internal/ollama"
	"goGioIa/internal/pdf"
	"goGioIa/internal/store"
)

// Prefijos de tarea de nomic-embed-text (exportados: la página de
// configuración del dashboard los muestra tal como se usan).
const (
	DocPrefix   = "search_document: "
	QueryPrefix = "search_query: "

	docPrefix   = DocPrefix
	queryPrefix = QueryPrefix
)

// maxSplitDepth acota cuántas veces se parte por la mitad un chunk que
// Ollama rechaza por tamaño (2^4 = 16 sub-trozos como máximo).
const maxSplitDepth = 4

// maxContextChunkChars recorta cada chunk citado en el prompt.
const maxContextChunkChars = 2000

// maxChunksPerDoc topa cuántos chunks de un mismo documento entran al top-K
// final (ver diversifySources).
const maxChunksPerDoc = 2

// embedFn abstrae la llamada de embeddings para poder probar la lógica de
// troceo y recuperación sin un cliente Ollama real.
type embedFn func(ctx context.Context, inputs []string) ([][]float32, error)

// Service orquesta ingesta y consultas RAG.
type Service struct {
	cfg    config.Config
	store  *store.Store
	ollama *ollama.Client
}

// New construye el servicio RAG.
func New(cfg config.Config, st *store.Store, ol *ollama.Client) *Service {
	return &Service{cfg: cfg, store: st, ollama: ol}
}

// Store expone el vector store (listar/eliminar documentos desde la API).
func (s *Service) Store() *store.Store { return s.store }

// ── Ingesta / entrenamiento ──────────────────────────────────────────────

// ErrDuplicate señala que el PDF (mismo SHA-256) ya está en la base.
type ErrDuplicate struct{ Doc *store.Document }

func (e *ErrDuplicate) Error() string {
	return fmt.Sprintf("el documento ya existe (%s, estado %s)", e.Doc.FileName, e.Doc.Status)
}

// IngestAsync registra el documento y lanza el procesamiento en segundo
// plano; el frontend sigue el avance consultando el estado. Si un intento
// anterior del mismo archivo quedó en FAILED, se reanuda sobre la misma fila:
// los chunks ya embebidos se conservan y el pipeline continúa desde el
// primero pendiente (los upserts garantizan que no se duplican vectores).
func (s *Service) IngestAsync(ctx context.Context, fileName string, data []byte, uploadedBy string) (string, error) {
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
		raw, err := store.ParseID(existing.ID)
		if err != nil {
			return "", fmt.Errorf("identificador del intento anterior: %w", err)
		}
		if err := s.store.SetDocumentStatus(ctx, raw, store.StatusExtracting, ""); err != nil {
			return "", fmt.Errorf("reanudar intento fallido anterior: %w", err)
		}
		go s.process(raw, fileName, data)
		return existing.ID, nil
	}

	docID, err := s.store.CreateDocument(ctx, fileName, hash, int64(len(data)), uploadedBy)
	if err != nil {
		return "", fmt.Errorf("registrar documento: %w", err)
	}

	go s.process(docID, fileName, data)

	return hex.EncodeToString(docID), nil
}

// process ejecuta el pipeline completo sobre un documento ya registrado:
// UPLOADED → EXTRACTING → CHUNKED → EMBEDDED (o FAILED con el motivo).
// Es reanudable: los chunks ya persistidos de un intento anterior se saltan.
func (s *Service) process(docID []byte, fileName string, data []byte) {
	// Independiente de la petición HTTP: la ingesta sobrevive al upload.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// Las llamadas de embeddings de esta ingesta quedan atribuidas al
	// documento en rag_events (dashboard de operaciones).
	ctx = ollama.WithPurpose(ollama.WithRef(ctx, docID), "ingest")

	started := time.Now()
	docHex := hex.EncodeToString(docID)
	stage := func(detail string, extra store.Event) {
		extra.Kind, extra.Ref, extra.OK, extra.Detail = "ingest", docID, true, detail
		s.store.RecordEvent(extra)
	}
	fail := func(stage string, err error) {
		log.Printf("rag: ingesta doc=%s (%q) falló en %s: %v", docHex, fileName, stage, err)
		msg := fmt.Sprintf("%s: %v", stage, err)
		if len(msg) > 3900 {
			msg = msg[:3900]
		}
		if err := s.store.SetDocumentStatus(ctx, docID, store.StatusFailed, msg); err != nil {
			log.Printf("rag: no se pudo marcar FAILED: %v", err)
		}
		s.store.RecordEvent(store.Event{
			Kind: "ingest", Ref: docID, OK: false, Detail: "failed:" + stage,
			ErrorKind: ollama.ClassifyError(err), ErrorDetail: err.Error(),
			Latency: time.Since(started),
		})
	}

	// 1. Extracción de texto (por página, para citar fuentes).
	if err := s.store.SetDocumentStatus(ctx, docID, store.StatusExtracting, ""); err != nil {
		fail("actualizar estado", err)
		return
	}
	stage("start", store.Event{PayloadBytes: len(data)})
	extractStart := time.Now()
	pages, err := pdf.ExtractPages(data)
	if err != nil {
		fail("extracción de texto", err)
		return
	}
	stage("extracted", store.Event{Latency: time.Since(extractStart), BatchSize: len(pages)})
	for _, p := range pages {
		if p.Text == "" {
			continue
		}
		if err := s.store.InsertPage(ctx, docID, p.Number, p.Text); err != nil {
			fail("guardar páginas", err)
			return
		}
	}

	// 2. Chunking + tope duro: ningún chunk puede exceder el contexto del
	// modelo de embeddings (estimación conservadora, prefijo incluido).
	chunks := chunkPages(pages, s.cfg.ChunkSize, s.cfg.ChunkOverlap)
	chunks = capChunks(chunks, ollama.MaxInputChars(s.cfg.EmbedMaxTokens)-len(docPrefix))
	if len(chunks) == 0 {
		fail("chunking", fmt.Errorf("el documento no produjo fragmentos de texto"))
		return
	}
	if err := s.store.SetDocumentStatus(ctx, docID, store.StatusChunked, ""); err != nil {
		fail("actualizar estado", err)
		return
	}
	stage("chunked", store.Event{BatchSize: len(chunks), TokensIn: estimateChunksTokens(chunks), TokenSource: "estimated"})

	// 3. Reanudación: se salta lo ya embebido en intentos anteriores.
	done, err := s.store.EmbeddedChunkIndexes(ctx, docID)
	if err != nil {
		fail("consultar avance previo", err)
		return
	}
	pending := pendingChunks(chunks, done)
	if len(chunks) > len(pending) {
		log.Printf("rag: doc=%s reanuda la ingesta: %d/%d chunks ya embebidos",
			docHex, len(chunks)-len(pending), len(chunks))
	}

	// 4. Embeddings por lotes + upsert en el vector store.
	if len(pending) > 0 {
		// Precalentamiento: garantiza que el modelo de embeddings está
		// cargado y respondiendo antes de encolar el documento completo.
		if err := s.ollama.WarmEmbed(ctx, s.cfg.EmbedModel); err != nil {
			fail("preparar modelo de embeddings", err)
			return
		}
	}
	embed := func(ctx context.Context, inputs []string) ([][]float32, error) {
		return s.ollama.Embed(ctx, s.cfg.EmbedModel, inputs)
	}
	batchSize := s.cfg.EmbedBatchSize
	if batchSize <= 0 {
		batchSize = 8
	}
	for from := 0; from < len(pending); from += batchSize {
		batch := pending[from:min(from+batchSize, len(pending))]
		if err := s.embedAndStore(ctx, docID, batch, embed); err != nil {
			fail("embeddings", err)
			return
		}
	}

	// 5. Poda de chunks sobrantes de intentos anteriores con otro troceo.
	if err := s.store.DeleteChunksFrom(ctx, docID, len(chunks)); err != nil {
		fail("podar chunks obsoletos", err)
		return
	}

	// 6. Listo solo cuando todos los chunks están embebidos y persistidos.
	if err := s.store.FinishDocument(ctx, docID, len(pages)); err != nil {
		fail("finalizar documento", err)
		return
	}
	stage("done", store.Event{Latency: time.Since(started), BatchSize: len(chunks)})
	log.Printf("rag: doc=%s (%q) ingerido (%d páginas, %d chunks)", docHex, fileName, len(pages), len(chunks))
}

// estimateChunksTokens suma los tokens estimados de un lote de chunks.
func estimateChunksTokens(chunks []chunk) int {
	total := 0
	for _, c := range chunks {
		total += estimateTokens(c.Text)
	}
	return total
}

// pendingChunks filtra los chunks cuyo índice ya tiene embedding persistido.
func pendingChunks(chunks []chunk, done map[int]bool) []chunk {
	if len(done) == 0 {
		return chunks
	}
	out := make([]chunk, 0, len(chunks))
	for _, c := range chunks {
		if !done[c.Index] {
			out = append(out, c)
		}
	}
	return out
}

// embedAndStore vectoriza un lote y lo persiste chunk a chunk. La persistencia
// es un upsert por (document_id, chunk_index), así que repetir un lote tras un
// fallo transitorio no duplica vectores. Si Ollama rechaza el lote con un 400
// por tamaño, se aísla cada chunk y el culpable se trocea (embedSplitting).
func (s *Service) embedAndStore(ctx context.Context, docID []byte, batch []chunk, embed embedFn) error {
	inputs := make([]string, len(batch))
	payloadBytes := 0
	for i, c := range batch {
		inputs[i] = docPrefix + c.Text
		payloadBytes += len(inputs[i])
	}
	docHex := hex.EncodeToString(docID)
	first, last := batch[0].Index, batch[len(batch)-1].Index

	start := time.Now()
	vectors, err := embed(ctx, inputs)
	if err != nil {
		httpErr, ok := errors.AsType[*ollama.HTTPError](err)
		if !ok || !httpErr.IsInputTooLarge() {
			return fmt.Errorf("chunks %d-%d (%d bytes): %w", first, last, payloadBytes, err)
		}
		// El 400 indica tamaño de entrada: chunk a chunk, troceando el culpable.
		log.Printf("rag: doc=%s chunks %d-%d rechazados por tamaño (HTTP 400: %s); troceando chunk a chunk",
			docHex, first, last, httpErr.Body)
		vectors = make([][]float32, len(batch))
		for i, c := range batch {
			vec, err := embedSplitting(ctx, embed, docPrefix, c.Text, maxSplitDepth)
			if err != nil {
				return fmt.Errorf("chunk %d (~%d tokens, %d bytes): %w",
					c.Index, estimateTokens(c.Text), len(c.Text), err)
			}
			vectors[i] = vec
		}
	}
	for i, c := range batch {
		if err := s.store.InsertChunk(ctx, docID, c.Index, c.Page, c.Text,
			estimateTokens(c.Text), vectors[i], s.cfg.EmbedModel); err != nil {
			return fmt.Errorf("persistir chunk %d: %w", c.Index, err)
		}
	}
	log.Printf("rag: doc=%s chunks %d-%d embebidos (n=%d bytes=%d latencia=%s)",
		docHex, first, last, len(batch), payloadBytes, time.Since(start).Round(time.Millisecond))
	return nil
}

// embedSplitting vectoriza un texto; si Ollama responde 400 por tamaño lo
// parte por la mitad, vectoriza los hijos (recursivo, con profundidad
// acotada) y promedia sus vectores — equivalente bajo distancia coseno — de
// modo que el chunk conserva su identidad, su índice y su texto completo.
func embedSplitting(ctx context.Context, embed embedFn, prefix, text string, depth int) ([]float32, error) {
	vecs, err := embed(ctx, []string{prefix + text})
	if err == nil {
		return vecs[0], nil
	}
	httpErr, ok := errors.AsType[*ollama.HTTPError](err)
	if !ok || !httpErr.IsInputTooLarge() || depth <= 0 {
		return nil, err
	}
	left, right := halveText(text)
	if left == "" || right == "" {
		return nil, err
	}
	lv, err := embedSplitting(ctx, embed, prefix, left, depth-1)
	if err != nil {
		return nil, err
	}
	rv, err := embedSplitting(ctx, embed, prefix, right, depth-1)
	if err != nil {
		return nil, err
	}
	return meanVec(lv, rv)
}

// halveText parte el texto cerca de la mitad, sobre un espacio si lo hay.
func halveText(text string) (string, string) {
	mid := len(text) / 2
	cut := strings.LastIndexAny(text[:mid], " \n\t")
	if cut <= 0 {
		cut = mid
	}
	return strings.TrimSpace(text[:cut]), strings.TrimSpace(text[cut:])
}

// meanVec promedia dos vectores de la misma dimensión.
func meanVec(a, b []float32) ([]float32, error) {
	if len(a) != len(b) {
		return nil, fmt.Errorf("dimensiones incompatibles al promediar (%d vs %d)", len(a), len(b))
	}
	out := make([]float32, len(a))
	for i := range a {
		out[i] = (a[i] + b[i]) / 2
	}
	return out, nil
}

// ── Consulta (retrieval + prompt) ────────────────────────────────────────

// Prepared contiene todo lo necesario para generar la respuesta en streaming
// y, al terminar, completar la trazabilidad en rag_queries.
type Prepared struct {
	QueryID  string               // hex, para feedback desde el frontend
	Sources  []store.SearchResult // chunks recuperados, ya ordenados
	Messages []ollama.Message     // prompt final para el LLM
	Model    string               // modelo con el que se generará
	Options  map[string]any       // parámetros del modelo
}

// PrepareAsk vectoriza la pregunta, recupera los chunks más afines desde
// Oracle 23ai, construye el prompt con la plantilla activa y deja registrada
// la consulta (rag_queries + rag_retrieved_chunks).
func (s *Service) PrepareAsk(ctx context.Context, question, model, sessionID, userID string) (*Prepared, error) {
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

	// 1. Embedding de la consulta (mismo espacio vectorial que los chunks).
	qVec, embedMeta, err := s.ollama.EmbedOneMeta(
		ollama.WithPurpose(ctx, "query"), s.cfg.EmbedModel, queryPrefix+question)
	if err != nil {
		return nil, fmt.Errorf("vectorizar la pregunta: %w", err)
	}

	// 2. Retrieval por similitud coseno. Se piden más candidatos de los que
	// entran al prompt para poder diversificar por documento: un manual
	// grande no debe acaparar todas las fuentes si otro documento también
	// tiene chunks afines (visto en producción: un manual de 714 chunks
	// desplazaba siempre al documento de 32 que tenía la respuesta).
	retrievalStart := time.Now()
	candidates, err := s.store.SearchChunks(ctx, qVec, s.cfg.RAGTopK*3)
	if err != nil {
		return nil, fmt.Errorf("búsqueda vectorial: %w", err)
	}
	sources := diversifySources(candidates, s.cfg.RAGTopK, maxChunksPerDoc)
	retrievalLatency := time.Since(retrievalStart)

	// 3. Prompt desde la plantilla activa (versionada en prompt_templates).
	templateID, templateText, err := s.store.ActiveTemplate(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("cargar plantilla de prompt: %w", err)
	}
	prompt := renderPrompt(templateText, question, sources)

	// 4. Trazabilidad: consulta + chunks usados.
	queryID, err := s.store.CreateQuery(ctx, store.SessionID(sessionID), userID, question, qVec, model, templateID)
	if err != nil {
		return nil, fmt.Errorf("registrar consulta: %w", err)
	}
	if err := s.store.LogRetrievedChunks(ctx, queryID, sources); err != nil {
		log.Printf("rag: no se pudieron registrar los chunks recuperados: %v", err)
	}

	// Eventos correlacionados con la consulta (dashboard de operaciones).
	// El evento «embed» sin ref del Recorder cubre la métrica de infraestructura;
	// estos dos aportan la correlación query_id y el desglose por etapa.
	queryTokens, tokenSource := embedMeta.Tokens, "ollama"
	if queryTokens == 0 {
		queryTokens, tokenSource = estimateTokens(queryPrefix+question), "estimated"
	}
	s.store.RecordEvent(store.Event{
		Kind: "query_embed", Ref: queryID, Model: s.cfg.EmbedModel, OK: true,
		Latency: embedMeta.Latency, Attempts: embedMeta.Attempts,
		TokensIn: queryTokens, TokenSource: tokenSource, LoadDuration: embedMeta.LoadDuration,
	})
	topScore := 0.0
	if len(sources) > 0 {
		topScore = sources[0].Similarity
	}
	s.store.RecordEvent(store.Event{
		Kind: "retrieval", Ref: queryID, OK: true,
		Latency: retrievalLatency, BatchSize: len(sources),
		Detail: fmt.Sprintf("topK=%d topScore=%.4f", s.cfg.RAGTopK, topScore),
	})

	return &Prepared{
		QueryID:  hex.EncodeToString(queryID),
		Sources:  sources,
		Messages: []ollama.Message{{Role: "user", Content: prompt}},
		Model:    model,
		Options:  map[string]any{"num_ctx": s.cfg.RAGNumCtx, "temperature": 0.2},
	}, nil
}

// FinishAsk completa la fila de rag_queries con la respuesta generada.
func (s *Service) FinishAsk(ctx context.Context, queryIDHex, response string) error {
	id, err := store.ParseID(queryIDHex)
	if err != nil {
		return err
	}
	return s.store.SetQueryResponse(ctx, id, response)
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
	return s.store.InsertFeedback(ctx, id, rating, comment, createdBy)
}

// diversifySources elige topK resultados respetando un máximo por documento
// (los candidatos vienen ordenados por afinidad). Si con el cupo no se llena
// el topK (p.ej. solo hay un documento), se rellena con los mejores restantes.
func diversifySources(candidates []store.SearchResult, topK, maxPerDoc int) []store.SearchResult {
	if len(candidates) <= topK {
		return candidates
	}
	out := make([]store.SearchResult, 0, topK)
	taken := make(map[int]bool, topK)
	perDoc := make(map[string]int)
	for i, c := range candidates {
		if len(out) == topK {
			return out
		}
		if perDoc[c.DocumentID] >= maxPerDoc {
			continue
		}
		perDoc[c.DocumentID]++
		taken[i] = true
		out = append(out, c)
	}
	for i, c := range candidates {
		if len(out) == topK {
			break
		}
		if !taken[i] {
			out = append(out, c)
		}
	}
	// Restaurar el orden por afinidad tras el relleno.
	sort.SliceStable(out, func(a, b int) bool { return out[a].Similarity > out[b].Similarity })
	return out
}

// renderPrompt sustituye {context} y {question} en la plantilla.
func renderPrompt(template, question string, sources []store.SearchResult) string {
	var b strings.Builder
	if len(sources) == 0 {
		b.WriteString("(La base de conocimiento no devolvió resultados para esta pregunta.)")
	}
	for i, src := range sources {
		text := src.Text
		if len(text) > maxContextChunkChars {
			text = text[:maxContextChunkChars] + "…"
		}
		fmt.Fprintf(&b, "[Fuente %d] %s (pág. %d)\n%s\n\n", i+1, src.FileName, src.PageNumber, text)
	}
	out := strings.ReplaceAll(template, "{context}", strings.TrimSpace(b.String()))
	out = strings.ReplaceAll(out, "{question}", question)
	return out
}
