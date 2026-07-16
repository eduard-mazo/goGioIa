package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"goGioIa/internal/pdf"
	"goGioIa/internal/store"
)

const (
	// attachInlineChars: hasta este tamaño el anexo se inyecta completo en el
	// prompt; por encima se vectoriza y se consulta por retrieval.
	attachInlineChars = 8000
	// attachTopK limita los fragmentos recuperados de anexos vectorizados.
	attachTopK = 4
)

// IngestAttachment guarda el anexo de una conversación (texto ya extraído) y,
// si es grande, encola su vectorización. Re-adjuntar el mismo contenido
// devuelve el id existente. Devuelve el id (hex) y el tamaño del texto.
func (s *Service) IngestAttachment(ctx context.Context, convIDHex, fileName string, data []byte) (string, int, error) {
	if !SupportedFile(fileName) {
		return "", 0, fmt.Errorf("tipo de archivo no admitido: %s", SupportedTypesMsg)
	}
	convID, err := store.ParseID(convIDHex)
	if err != nil {
		return "", 0, err
	}
	if err := s.store.EnsureReady(ctx); err != nil {
		return "", 0, err
	}
	text, err := ExtractText(fileName, data)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	id, existed, err := s.store.CreateAttachment(ctx, convID, fileName,
		hex.EncodeToString(sum[:]), MimeFor(fileName), text)
	if err != nil {
		return "", 0, fmt.Errorf("guardar anexo: %w", err)
	}
	if !existed && len(text) > attachInlineChars {
		if err := s.store.EnqueueJob(ctx, store.JobEmbedAttachment, id); err != nil {
			// Sin vectorizar sigue siendo usable (recortado en el prompt).
			log.Printf("rag: no se pudo encolar la vectorización de %q: %v", fileName, err)
		} else {
			s.wakeWorkers()
		}
	}
	return hex.EncodeToString(id), len(text), nil
}

// DeleteAttachment elimina un anexo de su conversación.
func (s *Service) DeleteAttachment(ctx context.Context, convIDHex, attIDHex string) error {
	convID, err := store.ParseID(convIDHex)
	if err != nil {
		return err
	}
	attID, err := store.ParseID(attIDHex)
	if err != nil {
		return err
	}
	if err := s.store.EnsureReady(ctx); err != nil {
		return err
	}
	return s.store.DeleteAttachment(ctx, convID, attID)
}

// runEmbedAttachment trocea y vectoriza un anexo grande (worker de la cola).
func (s *Service) runEmbedAttachment(attID []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	att, err := s.store.LoadAttachment(ctx, attID)
	if err != nil {
		return fmt.Errorf("cargar anexo: %w", err)
	}
	// Idempotencia en reintentos: descartar chunks parciales.
	if err := s.store.ClearAttachmentChunks(ctx, attID); err != nil {
		return fmt.Errorf("limpiar chunks previos: %w", err)
	}

	chunks := chunkPages([]pdf.Page{{Number: 0, Text: att.Content}}, s.cfg.ChunkSize, s.cfg.ChunkOverlap)
	if len(chunks) == 0 {
		return fmt.Errorf("el anexo no produjo fragmentos de texto")
	}
	batchSize := s.cfg.EmbedBatch
	for from := 0; from < len(chunks); from += batchSize {
		batch := chunks[from:min(from+batchSize, len(chunks))]
		inputs := make([]string, len(batch))
		for i, c := range batch {
			inputs[i] = docPrefix + c.Text
		}
		vectors, err := s.embedBatch(ctx, inputs)
		if err != nil {
			return fmt.Errorf("embeddings (chunks %d-%d de %d): %w", from+1, from+len(batch), len(chunks), err)
		}
		for i, c := range batch {
			if err := s.store.InsertAttachmentChunk(ctx, attID, c.Index, c.Text, vectors[i]); err != nil {
				return fmt.Errorf("guardar chunks: %w", err)
			}
		}
	}
	log.Printf("rag: anexo %q vectorizado (%d chunks)", att.FileName, len(chunks))
	return nil
}

// resolveAttachments materializa anexos referenciados por id para el prompt:
// los pequeños completos; los vectorizados vía retrieval con el embedding de
// la pregunta (embed es una función perezosa que puede devolver nil si no hay
// embedding disponible — entonces se inyectan recortados).
func (s *Service) resolveAttachments(ctx context.Context, ids []string, embed func() []float32) []AttachedDoc {
	var out []AttachedDoc
	var searchIDs [][]byte
	for _, hexID := range ids {
		id, err := store.ParseID(hexID)
		if err != nil {
			continue
		}
		att, err := s.store.LoadAttachment(ctx, id)
		if err != nil {
			log.Printf("rag: anexo %s no disponible: %v", hexID, err)
			continue
		}
		if att.Chunks > 0 {
			searchIDs = append(searchIDs, id)
			continue
		}
		text := att.Content
		if len(text) > attachInlineChars {
			// Grande pero aún sin vectorizar (job pendiente o fallido).
			text = text[:attachInlineChars] + "… (anexo recortado)"
		}
		out = append(out, AttachedDoc{Name: att.FileName, Text: text})
	}
	if len(searchIDs) == 0 {
		return out
	}
	qVec := embed()
	if qVec == nil {
		// Sin embedding: degradar a inyección recortada del contenido.
		for _, id := range searchIDs {
			if att, err := s.store.LoadAttachment(ctx, id); err == nil {
				out = append(out, AttachedDoc{Name: att.FileName, Text: att.Content[:min(attachInlineChars, len(att.Content))] + "…"})
			}
		}
		return out
	}
	res, err := s.store.SearchAttachmentChunks(ctx, searchIDs, qVec, attachTopK)
	if err != nil {
		log.Printf("rag: búsqueda en anexos falló: %v", err)
		return out
	}
	for _, r := range res {
		out = append(out, AttachedDoc{
			Name: fmt.Sprintf("%s (fragmento %d)", r.FileName, r.ChunkIndex+1),
			Text: r.Text,
		})
	}
	return out
}

// AttachmentContext resuelve anexos por id para el chat general. Si alguno
// está vectorizado, la pregunta se embebe (una vez) para recuperar solo los
// fragmentos afines. Best-effort: sin Oracle devuelve nil.
func (s *Service) AttachmentContext(ctx context.Context, ids []string, question string) []AttachedDoc {
	if len(ids) == 0 || !s.store.Ready() {
		return nil
	}
	var memo []float32
	embed := func() []float32 {
		if memo != nil {
			return memo
		}
		vec, err := s.ollama.EmbedOne(ctx, s.cfg.EmbedModel, queryPrefix+question)
		if err != nil {
			log.Printf("rag: no se pudo vectorizar la pregunta para los anexos: %v", err)
			return nil
		}
		memo = vec
		return vec
	}
	return s.resolveAttachments(ctx, ids, embed)
}
