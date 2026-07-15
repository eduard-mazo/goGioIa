package rag

import (
	"context"
	"encoding/hex"
	"log"
	"time"

	"goGioIa/internal/store"
)

// claimInterval es el sondeo de respaldo de los workers: recoge trabajos
// aunque se pierda la señal de wake (p.ej. reencolados tras un reinicio).
const claimInterval = 15 * time.Second

// Start arranca la cola de ingesta: reencola los trabajos huérfanos de un
// reinicio y lanza los workers y la purga de historial. Respeta ctx para un
// apagado ordenado (hoy el proceso vive hasta que lo matan, pero los workers
// no deben impedir tests ni futuros graceful shutdowns).
func (s *Service) Start(ctx context.Context) {
	go func() {
		// Esperar a que Oracle esté disponible antes de tocar la cola.
		for {
			if err := s.store.EnsureReady(ctx); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
		}
		if n, err := s.store.RequeueOrphanJobs(ctx); err != nil {
			log.Printf("rag: no se pudieron reencolar trabajos huérfanos: %v", err)
		} else if n > 0 {
			log.Printf("rag: %d trabajo(s) interrumpido(s) por el reinicio, reencolados", n)
		}
		workers := max(s.cfg.RAGWorkers, 1)
		for i := 1; i <= workers; i++ {
			go s.worker(ctx, i)
		}
		go s.purgeLoop(ctx)
		log.Printf("rag: cola de ingesta activa (%d workers)", workers)
	}()
}

// worker consume la cola hasta vaciarla y espera (señal o sondeo) trabajo nuevo.
func (s *Service) worker(ctx context.Context, id int) {
	ticker := time.NewTicker(claimInterval)
	defer ticker.Stop()
	for {
		s.drainQueue(ctx, id)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

// drainQueue procesa trabajos de ingesta hasta que la cola queda vacía.
func (s *Service) drainQueue(ctx context.Context, workerID int) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := s.store.ClaimJob(ctx, store.JobIngestDocument)
		if err != nil {
			log.Printf("rag: worker %d no pudo reclamar trabajo: %v", workerID, err)
			return
		}
		if job == nil {
			return
		}
		s.runJob(ctx, workerID, job)
	}
}

// runJob ejecuta un trabajo de ingesta reclamado y cierra su resultado.
func (s *Service) runJob(ctx context.Context, workerID int, job *store.Job) {
	docHex := hex.EncodeToString(job.PayloadID)
	fileName, data, err := s.store.LoadDocumentFile(ctx, job.PayloadID)
	if err != nil {
		// Documento o archivo desaparecido (p.ej. eliminado mientras esperaba).
		log.Printf("rag: worker %d descarta trabajo de %s: %v", workerID, docHex, err)
		if err := s.store.FinishJob(ctx, job.ID, "archivo de origen no disponible: "+err.Error()); err != nil {
			log.Printf("rag: no se pudo cerrar el trabajo: %v", err)
		}
		return
	}

	log.Printf("rag: worker %d procesa %q (intento %d)", workerID, fileName, job.Attempts)
	ingestErr := s.runIngest(job.PayloadID, fileName, data)

	msg := ""
	if ingestErr != nil {
		msg = ingestErr.Error()
	}
	if err := s.store.FinishJob(ctx, job.ID, msg); err != nil {
		log.Printf("rag: no se pudo cerrar el trabajo de %q: %v", fileName, err)
	}
	if ingestErr == nil {
		// Ingesta completa: el BLOB original ya no hace falta.
		if err := s.store.DeleteDocumentFile(ctx, job.PayloadID); err != nil {
			log.Printf("rag: no se pudo liberar el archivo de %q: %v", fileName, err)
		}
	}
}

// RetryDocument reencola la ingesta de un documento fallido.
func (s *Service) RetryDocument(ctx context.Context, docIDHex string) error {
	id, err := store.ParseID(docIDHex)
	if err != nil {
		return err
	}
	if err := s.store.EnsureReady(ctx); err != nil {
		return err
	}
	if err := s.store.RequeueDocument(ctx, id); err != nil {
		return err
	}
	s.wakeWorkers()
	return nil
}

// purgeLoop aplica la retención del historial de consultas (rag_queries y,
// por CASCADE, rag_retrieved_chunks y rag_feedback) una vez al día.
func (s *Service) purgeLoop(ctx context.Context) {
	days := s.cfg.HistoryRetentionDays
	if days <= 0 {
		return
	}
	retention := time.Duration(days) * 24 * time.Hour
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		if n, err := s.store.PurgeOldQueries(ctx, retention); err != nil {
			log.Printf("rag: purga de historial falló: %v", err)
		} else if n > 0 {
			log.Printf("rag: purgadas %d consultas con más de %d días", n, days)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
