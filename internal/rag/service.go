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
		raw, err := store.ParseID(existing.ID)
		if err == nil {
			if err := s.store.DeleteDocument(ctx, raw); err != nil {
				return "", fmt.Errorf("limpiar intento fallido anterior: %w", err)
			}
		}
	}

	docID, err := s.store.CreateDocument(ctx, fileName, hash, MimeFor(fileName), uploadedBy, data)
	if err != nil {
		return "", fmt.Errorf("registrar documento: %w", err)
	}

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
	if err := s.store.SetDocumentStatus(ctx, docID, store.StatusChunked, ""); err != nil {
		return fail("actualizar estado", err)
	}

	// 3. Embeddings por lotes + inserción en el vector store.
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
	log.Printf("rag: %q ingerido (%d páginas, %d chunks)", fileName, pageCount, len(chunks))
	return nil
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
}

// PrepareAsk vectoriza la pregunta, recupera los chunks más afines desde
// Oracle 23ai, construye el prompt con la plantilla activa (incluyendo los
// documentos adjuntos de la conversación, si los hay) y deja registrada la
// consulta (rag_queries + rag_retrieved_chunks).
func (s *Service) PrepareAsk(ctx context.Context, question, model, sessionID, userID string, attached []AttachedDoc) (*Prepared, error) {
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
	qVec, err := s.ollama.EmbedOne(ctx, s.cfg.EmbedModel, queryPrefix+question)
	if err != nil {
		return nil, fmt.Errorf("vectorizar la pregunta: %w", err)
	}

	// 2. Retrieval por similitud coseno.
	sources, err := s.store.SearchChunks(ctx, qVec, s.cfg.RAGTopK)
	if err != nil {
		return nil, fmt.Errorf("búsqueda vectorial: %w", err)
	}

	// 3. Prompt desde la plantilla activa (versionada en prompt_templates).
	templateID, templateText, err := s.store.ActiveTemplate(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("cargar plantilla de prompt: %w", err)
	}
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
	return &Prepared{
		QueryID:  hex.EncodeToString(queryID),
		Sources:  sources,
		Messages: []ollama.Message{{Role: "user", Content: prompt}},
		Model:    model,
		Options:  map[string]any{"num_ctx": numCtx, "temperature": 0.2},
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
