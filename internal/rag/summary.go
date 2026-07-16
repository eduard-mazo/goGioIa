package rag

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"goGioIa/internal/ollama"
	"goGioIa/internal/store"
)

const (
	// summaryTriggerFactor: se resume cuando lo no resumido supera este
	// múltiplo de la ventana de historial (cadencia ≈ una ventana por job).
	summaryTriggerFactor = 2
	// maxSummaryChars acota el resumen guardado y el inyectado en prompts.
	maxSummaryChars = 4000
	// maxSummaryMsgChars recorta cada mensaje dentro de la transcripción.
	maxSummaryMsgChars = 1000
	// maxSummaryTranscript acota la transcripción total enviada al modelo.
	maxSummaryTranscript = 12000
)

// MaybeSummarize encola (una sola vez) el resumen de la conversación cuando
// acumula suficientes mensajes fuera de la ventana de historial. Best-effort:
// se llama tras persistir cada respuesta.
func (s *Service) MaybeSummarize(ctx context.Context, convID []byte) {
	if len(convID) == 0 || !s.store.Ready() {
		return
	}
	_, upto, maxSeq, err := s.store.SummaryState(ctx, convID)
	if err != nil {
		log.Printf("rag: no se pudo evaluar el resumen de la conversación: %v", err)
		return
	}
	if maxSeq-upto <= s.cfg.HistoryWindow*summaryTriggerFactor {
		return
	}
	if queued, err := s.store.EnqueueJobOnce(ctx, store.JobSummarizeConversation, convID); err != nil {
		log.Printf("rag: no se pudo encolar el resumen: %v", err)
	} else if queued {
		s.wakeWorkers()
	}
}

// runSummaryJob ejecuta un trabajo de resumen reclamado y cierra su resultado.
func (s *Service) runSummaryJob(ctx context.Context, workerID int, job *store.Job) {
	msg := ""
	if err := s.runSummarize(job.PayloadID); err != nil {
		log.Printf("rag: worker %d no pudo resumir la conversación: %v", workerID, err)
		msg = err.Error()
	}
	if err := s.store.FinishJob(ctx, job.ID, msg); err != nil {
		log.Printf("rag: no se pudo cerrar el trabajo de resumen: %v", err)
	}
}

// runSummarize actualiza el resumen rodante: condensa el resumen previo más
// los mensajes que quedaron fuera de la ventana de historial.
func (s *Service) runSummarize(convID []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	prev, upto, maxSeq, err := s.store.SummaryState(ctx, convID)
	if err != nil {
		return fmt.Errorf("leer estado del resumen: %w", err)
	}
	cutoff := maxSeq - s.cfg.HistoryWindow
	if cutoff <= upto {
		return nil // la ventana aún cubre todo lo no resumido
	}
	msgs, err := s.store.MessagesBetween(ctx, convID, upto, cutoff)
	if err != nil {
		return fmt.Errorf("leer mensajes a resumir: %w", err)
	}
	if len(msgs) == 0 {
		// Hueco sin mensajes (purgas): avanzar el corte evita re-encolar.
		return s.store.SetSummary(ctx, convID, prev, cutoff)
	}

	prompt := buildSummaryPrompt(prev, msgs)
	out, err := s.ollama.Complete(ctx, s.cfg.RAGModel,
		[]ollama.Message{{Role: "user", Content: prompt}},
		map[string]any{"num_ctx": 8192, "temperature": 0.2})
	if err != nil {
		return fmt.Errorf("generar resumen: %w", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return fmt.Errorf("el modelo devolvió un resumen vacío")
	}
	if len(out) > maxSummaryChars {
		out = out[:maxSummaryChars]
	}
	if err := s.store.SetSummary(ctx, convID, out, cutoff); err != nil {
		return fmt.Errorf("guardar resumen: %w", err)
	}
	log.Printf("rag: conversación resumida hasta el mensaje %d (%d mensajes condensados)", cutoff, len(msgs))
	return nil
}

// buildSummaryPrompt arma la instrucción de resumen incremental.
func buildSummaryPrompt(prev string, msgs []store.ConvMessage) string {
	var b strings.Builder
	b.WriteString("Mantienes el resumen de una conversación entre un usuario y un asistente.\n\n")
	if prev != "" {
		b.WriteString("### Resumen previo\n")
		b.WriteString(prev)
		b.WriteString("\n\n")
	}
	b.WriteString("### Mensajes nuevos\n")
	budget := maxSummaryTranscript
	for _, m := range msgs {
		if budget <= 0 {
			break
		}
		role := "Usuario"
		if m.Role == "assistant" {
			role = "Asistente"
		}
		text := m.Content
		if len(text) > maxSummaryMsgChars {
			text = text[:maxSummaryMsgChars] + "…"
		}
		budget -= len(text)
		fmt.Fprintf(&b, "%s: %s\n", role, text)
	}
	b.WriteString("\n### Instrucción\n")
	b.WriteString("Escribe UN único resumen actualizado en español (máximo 150 palabras) que " +
		"combine el resumen previo con los mensajes nuevos. Conserva hechos, datos, decisiones " +
		"y pendientes importantes. Responde solo con el resumen, sin preámbulos.")
	return b.String()
}
