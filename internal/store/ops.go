package store

// Capa de métricas del dashboard de operaciones RAG. Todas las métricas se
// calculan aquí y solo aquí (una definición por métrica), con agregación en
// SQL sobre Oracle. Los conteos de tokens llevan siempre su fuente:
// «ollama» (autoritativo, reportado por el host) o «estimated» (≈4 chars por
// token, ver rag.estimateTokens); nunca se combinan sin etiquetar.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// rangeHours acota el rango temporal consultable (1 hora … 30 días).
func rangeHours(h int) int {
	if h < 1 {
		return 24
	}
	if h > 720 {
		return 720
	}
	return h
}

// ── Overview ─────────────────────────────────────────────────────────────

// Overview alimenta las tarjetas de la vista principal del dashboard.
type Overview struct {
	// Documentos y chunks: estado global actual (no dependen del rango).
	Docs struct {
		Total      int `json:"total"`
		Embedded   int `json:"embedded"`
		Processing int `json:"processing"`
		Failed     int `json:"failed"`
	} `json:"docs"`
	Chunks struct {
		Total     int   `json:"total"`
		Embedded  int   `json:"embedded"`
		Missing   int   `json:"missing"`
		EstTokens int64 `json:"estTokens"` // fuente: estimated
	} `json:"chunks"`

	// Lo que sigue se calcula dentro del rango seleccionado.
	Hours   int `json:"hours"`
	Queries struct {
		Total         int `json:"total"`
		Answered      int `json:"answered"`
		NoResults     int `json:"noResults"`
		WeakRetrieval int `json:"weakRetrieval"` // top score < umbral
		FeedbackUp    int `json:"feedbackUp"`
		FeedbackDown  int `json:"feedbackDown"`
	} `json:"queries"`
	Embeds     OpAgg   `json:"embeds"`     // kind=embed
	Generation OpAgg   `json:"generation"` // kind=generation
	ErrorRate  float64 `json:"errorRate"`  // errores/total sobre todos los kinds de llamada

	// Período anterior equivalente: solo para deltas reales, nunca inventados.
	Prev struct {
		HasData       bool  `json:"hasData"`
		Queries       int   `json:"queries"`
		EmbedRequests int   `json:"embedRequests"`
		Failures      int   `json:"failures"`
		TokensIn      int64 `json:"tokensIn"`
		TokensOut     int64 `json:"tokensOut"`
	} `json:"prev"`
}

// OpAgg agrega las llamadas de un kind dentro del rango.
type OpAgg struct {
	Requests  int   `json:"requests"`
	Failures  int   `json:"failures"`
	Retries   int   `json:"retries"`
	TokensIn  int64 `json:"tokensIn"`  // fuente: ollama
	TokensOut int64 `json:"tokensOut"` // fuente: ollama
	P50Ms     int64 `json:"p50Ms"`
	P95Ms     int64 `json:"p95Ms"`
	MaxMs     int64 `json:"maxMs"`
}

// OpsOverview calcula las tarjetas del resumen. weakThreshold es el umbral de
// similitud bajo el cual un retrieval se considera débil (exploratorio).
func (s *Store) OpsOverview(ctx context.Context, hours int, weakThreshold float64) (*Overview, error) {
	hours = rangeHours(hours)
	ov := &Overview{Hours: hours}

	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM documents GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("documentos por estado: %w", err)
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		ov.Docs.Total += n
		switch st {
		case StatusEmbedded:
			ov.Docs.Embedded += n
		case StatusFailed:
			ov.Docs.Failed += n
		default:
			ov.Docs.Processing += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(embedding), NVL(SUM(token_count), 0) FROM document_chunks`).
		Scan(&ov.Chunks.Total, &ov.Chunks.Embedded, &ov.Chunks.EstTokens); err != nil {
		return nil, fmt.Errorf("agregado de chunks: %w", err)
	}
	ov.Chunks.Missing = ov.Chunks.Total - ov.Chunks.Embedded

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(response_text)
		FROM rag_queries WHERE created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')`, hours).
		Scan(&ov.Queries.Total, &ov.Queries.Answered); err != nil {
		return nil, fmt.Errorf("agregado de consultas: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM rag_queries q
		WHERE q.created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
		  AND NOT EXISTS (SELECT 1 FROM rag_retrieved_chunks rc WHERE rc.query_id = q.query_id)`, hours).
		Scan(&ov.Queries.NoResults); err != nil {
		return nil, fmt.Errorf("consultas sin resultados: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT MAX(rc.similarity_score) top_score
			FROM rag_queries q
			JOIN rag_retrieved_chunks rc ON rc.query_id = q.query_id
			WHERE q.created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
			GROUP BY q.query_id
		) WHERE top_score < :2`, hours, weakThreshold).
		Scan(&ov.Queries.WeakRetrieval); err != nil {
		return nil, fmt.Errorf("retrieval débil: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT NVL(SUM(CASE WHEN rating > 0 THEN 1 ELSE 0 END), 0),
		       NVL(SUM(CASE WHEN rating < 0 THEN 1 ELSE 0 END), 0)
		FROM rag_feedback WHERE created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')`, hours).
		Scan(&ov.Queries.FeedbackUp, &ov.Queries.FeedbackDown); err != nil {
		return nil, fmt.Errorf("feedback: %w", err)
	}

	for _, k := range []struct {
		kind string
		dst  *OpAgg
	}{{"embed", &ov.Embeds}, {"generation", &ov.Generation}} {
		if err := s.opAgg(ctx, k.kind, hours, k.dst); err != nil {
			return nil, err
		}
	}

	var evTotal, evErr int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), NVL(SUM(CASE WHEN status = 'ERROR' THEN 1 ELSE 0 END), 0)
		FROM rag_events
		WHERE kind IN ('embed', 'generation', 'retrieval', 'query_embed')
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')`, hours).
		Scan(&evTotal, &evErr); err != nil {
		return nil, fmt.Errorf("tasa de error: %w", err)
	}
	if evTotal > 0 {
		ov.ErrorRate = float64(evErr) / float64(evTotal)
	}

	// Período anterior equivalente (para deltas honestos en las tarjetas).
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM rag_queries
		WHERE created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
		  AND created_at <  SYSTIMESTAMP - NUMTODSINTERVAL(:2, 'HOUR')`, hours*2, hours).
		Scan(&ov.Prev.Queries); err != nil {
		return nil, fmt.Errorf("consultas del período anterior: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       NVL(SUM(CASE WHEN status = 'ERROR' THEN 1 ELSE 0 END), 0),
		       NVL(SUM(tokens_in), 0), NVL(SUM(tokens_out), 0)
		FROM rag_events
		WHERE kind IN ('embed', 'generation', 'retrieval', 'query_embed')
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
		  AND created_at <  SYSTIMESTAMP - NUMTODSINTERVAL(:2, 'HOUR')`, hours*2, hours).
		Scan(&ov.Prev.EmbedRequests, &ov.Prev.Failures, &ov.Prev.TokensIn, &ov.Prev.TokensOut); err != nil {
		return nil, fmt.Errorf("eventos del período anterior: %w", err)
	}
	ov.Prev.HasData = ov.Prev.Queries > 0 || ov.Prev.EmbedRequests > 0

	return ov, nil
}

func (s *Store) opAgg(ctx context.Context, kind string, hours int, dst *OpAgg) error {
	var p50, p95 sql.NullFloat64
	var maxMs sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       NVL(SUM(CASE WHEN status = 'ERROR' THEN 1 ELSE 0 END), 0),
		       NVL(SUM(GREATEST(NVL(attempts, 1) - 1, 0)), 0),
		       NVL(SUM(tokens_in), 0), NVL(SUM(tokens_out), 0),
		       PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY latency_ms),
		       PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms),
		       MAX(latency_ms)
		FROM rag_events
		WHERE kind = :1 AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:2, 'HOUR')`, kind, hours).
		Scan(&dst.Requests, &dst.Failures, &dst.Retries, &dst.TokensIn, &dst.TokensOut, &p50, &p95, &maxMs)
	if err != nil {
		return fmt.Errorf("agregado %s: %w", kind, err)
	}
	dst.P50Ms = int64(p50.Float64)
	dst.P95Ms = int64(p95.Float64)
	dst.MaxMs = maxMs.Int64
	return nil
}

// ── Series temporales ────────────────────────────────────────────────────

// SeriesPoint es un punto (bucket, valor) de una serie temporal.
type SeriesPoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// KindCount es un conteo etiquetado (categoría de error, estado HTTP…).
type KindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// Timeseries agrupa las series del rango pedido, ya bucketizadas. Las series
// de conteo/tokens se rellenan con ceros; las de latencia son dispersas
// (solo buckets con datos), porque un cero de latencia sería una mentira.
type Timeseries struct {
	Hours         int           `json:"hours"`
	BucketSeconds int           `json:"bucketSeconds"`
	DocsCompleted []SeriesPoint `json:"docsCompleted"`
	EmbedOK       []SeriesPoint `json:"embedOk"`
	EmbedFail     []SeriesPoint `json:"embedFail"`
	EmbedTokens   []SeriesPoint `json:"embedTokens"` // fuente: ollama
	EmbedAvgMs    []SeriesPoint `json:"embedAvgMs"`
	GenOK         []SeriesPoint `json:"genOk"`
	GenFail       []SeriesPoint `json:"genFail"`
	GenTokens     []SeriesPoint `json:"genTokens"` // in+out, fuente: ollama
	GenAvgMs      []SeriesPoint `json:"genAvgMs"`
	Queries       []SeriesPoint `json:"queries"`
	ErrorsByKind  []KindCount   `json:"errorsByKind"`
}

// bucketExpr trunca una columna de tiempo a buckets de secs segundos. El
// tamaño va inline como literal (es un entero calculado por el servidor, no
// entrada del usuario): así la expresión del SELECT y la del GROUP BY son
// textualmente idénticas, como exige Oracle (ORA-00979 con binds distintos).
func bucketExpr(col string, secs int) string {
	return fmt.Sprintf(
		"TRUNC(CAST(%[1]s AS DATE)) + FLOOR((CAST(%[1]s AS DATE) - TRUNC(CAST(%[1]s AS DATE))) * 86400 / %[2]d) * %[2]d / 86400",
		col, secs)
}

// OpsTimeseries devuelve las series temporales del rango. bucketSecs se
// deriva del rango para producir ~48 puntos.
func (s *Store) OpsTimeseries(ctx context.Context, hours int) (*Timeseries, error) {
	hours = rangeHours(hours)
	bucket := (hours * 3600) / 48
	if bucket < 300 {
		bucket = 300
	}
	bucket -= bucket % 300

	ts := &Timeseries{Hours: hours, BucketSeconds: bucket}

	// Hora del servidor Oracle: el eje temporal se ancla al reloj de la BD
	// (todos los created_at son SYSTIMESTAMP), no al del proceso Go.
	var dbNow time.Time
	if err := s.db.QueryRowContext(ctx, `SELECT CAST(SYSTIMESTAMP AS DATE) FROM dual`).Scan(&dbNow); err != nil {
		return nil, fmt.Errorf("hora de la base de datos: %w", err)
	}

	// Eventos embed/generation por bucket y estado.
	be := bucketExpr("created_at", bucket)
	q := `SELECT ` + be + ` b, kind, status, COUNT(*),
	       NVL(SUM(tokens_in), 0), NVL(SUM(tokens_out), 0), NVL(AVG(latency_ms), 0)
	FROM rag_events
	WHERE kind IN ('embed', 'generation')
	  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
	GROUP BY ` + be + `, kind, status
	ORDER BY b`
	rows, err := s.db.QueryContext(ctx, q, hours)
	if err != nil {
		return nil, fmt.Errorf("serie de eventos: %w", err)
	}
	type evPoint struct {
		t                  time.Time
		kind, status       string
		n                  int
		tokIn, tokOut      int64
		avgMs              float64
	}
	var evPoints []evPoint
	for rows.Next() {
		var p evPoint
		if err := rows.Scan(&p.t, &p.kind, &p.status, &p.n, &p.tokIn, &p.tokOut, &p.avgMs); err != nil {
			rows.Close()
			return nil, err
		}
		evPoints = append(evPoints, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Consultas por bucket.
	qq := `SELECT ` + be + ` b, COUNT(*) FROM rag_queries
	WHERE created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
	GROUP BY ` + be + ` ORDER BY b`
	queryCounts, err := s.bucketCounts(ctx, qq, hours)
	if err != nil {
		return nil, fmt.Errorf("serie de consultas: %w", err)
	}

	// Documentos completados por bucket (processed_at real).
	bp := bucketExpr("processed_at", bucket)
	dq := `SELECT ` + bp + ` b, COUNT(*) FROM documents
	WHERE status = 'EMBEDDED' AND processed_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
	GROUP BY ` + bp + ` ORDER BY b`
	docCounts, err := s.bucketCounts(ctx, dq, hours)
	if err != nil {
		return nil, fmt.Errorf("serie de documentos: %w", err)
	}

	// Errores por categoría (rango completo, sin bucketizar).
	erows, err := s.db.QueryContext(ctx, `
		SELECT NVL(error_kind, 'desconocido'), COUNT(*)
		FROM rag_events
		WHERE status = 'ERROR' AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
		GROUP BY error_kind ORDER BY COUNT(*) DESC`, hours)
	if err != nil {
		return nil, fmt.Errorf("errores por categoría: %w", err)
	}
	for erows.Next() {
		var kc KindCount
		if err := erows.Scan(&kc.Kind, &kc.Count); err != nil {
			erows.Close()
			return nil, err
		}
		ts.ErrorsByKind = append(ts.ErrorsByKind, kc)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, err
	}

	// Rejilla completa de buckets, alineada al reloj de la BD.
	step := time.Duration(bucket) * time.Second
	end := dbNow.Truncate(step)
	start := end.Add(-time.Duration(hours) * time.Hour).Truncate(step)
	grid := map[time.Time]int{}
	var axis []time.Time
	for t := start; !t.After(end); t = t.Add(step) {
		grid[t] = len(axis)
		axis = append(axis, t)
	}
	zeroSeries := func() []SeriesPoint {
		out := make([]SeriesPoint, len(axis))
		for i, t := range axis {
			out[i] = SeriesPoint{T: t}
		}
		return out
	}
	ts.EmbedOK, ts.EmbedFail, ts.EmbedTokens = zeroSeries(), zeroSeries(), zeroSeries()
	ts.GenOK, ts.GenFail, ts.GenTokens = zeroSeries(), zeroSeries(), zeroSeries()
	ts.Queries, ts.DocsCompleted = zeroSeries(), zeroSeries()

	for _, p := range evPoints {
		i, ok := grid[p.t]
		if !ok {
			continue
		}
		switch {
		case p.kind == "embed" && p.status == "OK":
			ts.EmbedOK[i].V += float64(p.n)
			ts.EmbedTokens[i].V += float64(p.tokIn)
			ts.EmbedAvgMs = append(ts.EmbedAvgMs, SeriesPoint{T: p.t, V: p.avgMs})
		case p.kind == "embed":
			ts.EmbedFail[i].V += float64(p.n)
		case p.kind == "generation" && p.status == "OK":
			ts.GenOK[i].V += float64(p.n)
			ts.GenTokens[i].V += float64(p.tokIn + p.tokOut)
			ts.GenAvgMs = append(ts.GenAvgMs, SeriesPoint{T: p.t, V: p.avgMs})
		case p.kind == "generation":
			ts.GenFail[i].V += float64(p.n)
		}
	}
	for t, n := range queryCounts {
		if i, ok := grid[t]; ok {
			ts.Queries[i].V = float64(n)
		}
	}
	for t, n := range docCounts {
		if i, ok := grid[t]; ok {
			ts.DocsCompleted[i].V = float64(n)
		}
	}
	return ts, nil
}

func (s *Store) bucketCounts(ctx context.Context, query string, hours int) (map[time.Time]int, error) {
	rows, err := s.db.QueryContext(ctx, query, hours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[time.Time]int{}
	for rows.Next() {
		var t time.Time
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

// ── Documentos (tabla de ingesta) ────────────────────────────────────────

// DocFilter filtra/pagina la tabla de documentos.
type DocFilter struct {
	Status      string // estado exacto, o el meta-estado "PROCESSING"
	Query       string // LIKE sobre el nombre de archivo
	OnlyErrors  bool
	OnlyMissing bool // documentos con chunks sin embedding
	Hours       int  // 0 = sin límite temporal
	Sort        string
	Desc        bool
	Offset      int
	Limit       int
}

// DocRow es una fila de la tabla de ingesta con sus agregados de chunks.
type DocRow struct {
	Document
	EmbeddedChunks int    `json:"embeddedChunks"`
	MissingChunks  int    `json:"missingChunks"`
	EstTokens      int64  `json:"estTokens"` // fuente: estimated
	MinChunkTokens int    `json:"minChunkTokens"`
	MaxChunkTokens int    `json:"maxChunkTokens"`
	Models         string `json:"models"` // modelos de embedding usados (CSV)
	DurationSec    int64  `json:"durationSec"`
}

// DocPage es una página de la tabla de documentos.
type DocPage struct {
	Total  int      `json:"total"`
	Offset int      `json:"offset"`
	Limit  int      `json:"limit"`
	Rows   []DocRow `json:"rows"`
}

var docSortCols = map[string]string{
	"uploaded": "d.uploaded_at",
	"name":     "LOWER(d.file_name)",
	"size":     "NVL(d.file_size_bytes, 0)",
	"pages":    "NVL(d.page_count, 0)",
	"chunks":   "NVL(c.chunks, 0)",
	"status":   "d.status",
	"tokens":   "NVL(c.est_tokens, 0)",
}

// OpsDocuments devuelve la tabla de ingesta paginada, filtrada y ordenada.
func (s *Store) OpsDocuments(ctx context.Context, f DocFilter) (*DocPage, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 25
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf(":%d", len(args))
	}
	switch f.Status {
	case "":
	case "PROCESSING":
		where = append(where, "d.status IN ('UPLOADED', 'EXTRACTING', 'CHUNKED')")
	default:
		where = append(where, "d.status = "+arg(f.Status))
	}
	if f.Query != "" {
		where = append(where, "UPPER(d.file_name) LIKE UPPER("+arg("%"+f.Query+"%")+")")
	}
	if f.OnlyErrors {
		where = append(where, "d.status = 'FAILED'")
	}
	if f.OnlyMissing {
		where = append(where, "NVL(c.chunks, 0) > NVL(c.embedded, 0)")
	}
	if f.Hours > 0 {
		where = append(where, "d.uploaded_at >= SYSTIMESTAMP - NUMTODSINTERVAL("+arg(rangeHours(f.Hours))+", 'HOUR')")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	const fromSQL = `
	FROM documents d
	LEFT JOIN (
		SELECT document_id, COUNT(*) chunks, COUNT(embedding) embedded,
		       SUM(NVL(token_count, 0)) est_tokens,
		       MIN(token_count) min_t, MAX(token_count) max_t,
		       LISTAGG(DISTINCT embedding_model, ',') models
		FROM document_chunks GROUP BY document_id
	) c ON c.document_id = d.document_id `

	page := &DocPage{Offset: f.Offset, Limit: f.Limit, Rows: []DocRow{}}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*)"+fromSQL+whereSQL, args...).Scan(&page.Total); err != nil {
		return nil, fmt.Errorf("total de documentos: %w", err)
	}

	sortCol, ok := docSortCols[f.Sort]
	if !ok {
		sortCol, f.Desc = docSortCols["uploaded"], true
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	listArgs := append(append([]any{}, args...), f.Offset, f.Limit)
	query := `
	SELECT RAWTOHEX(d.document_id), d.file_name, NVL(d.file_size_bytes, 0),
	       NVL(d.page_count, 0), d.status, NVL(d.error_message, ' '),
	       NVL(d.uploaded_by, ' '), d.uploaded_at, d.processed_at,
	       NVL(c.chunks, 0), NVL(c.embedded, 0), NVL(c.est_tokens, 0),
	       NVL(c.min_t, 0), NVL(c.max_t, 0), NVL(c.models, ' ')` +
		fromSQL + whereSQL +
		fmt.Sprintf(" ORDER BY %s %s, d.document_id OFFSET :%d ROWS FETCH NEXT :%d ROWS ONLY",
			sortCol, dir, len(args)+1, len(args)+2)
	rows, err := s.db.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, fmt.Errorf("listar documentos: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r DocRow
		var errMsg, by, models string
		var processed sql.NullTime
		if err := rows.Scan(&r.ID, &r.FileName, &r.SizeBytes, &r.PageCount, &r.Status,
			&errMsg, &by, &r.UploadedAt, &processed,
			&r.ChunkCount, &r.EmbeddedChunks, &r.EstTokens,
			&r.MinChunkTokens, &r.MaxChunkTokens, &models); err != nil {
			return nil, err
		}
		r.ID = strings.ToLower(r.ID)
		r.Error = strings.TrimSpace(errMsg)
		r.UploadedBy = strings.TrimSpace(by)
		r.Models = strings.TrimSpace(models)
		r.MissingChunks = r.ChunkCount - r.EmbeddedChunks
		if processed.Valid {
			t := processed.Time
			r.ProcessedAt = &t
			r.DurationSec = int64(t.Sub(r.UploadedAt).Seconds())
		}
		page.Rows = append(page.Rows, r)
	}
	return page, rows.Err()
}

// ── Detalle de documento ─────────────────────────────────────────────────

// HistBin es un bin del histograma de tamaño de chunk (en tokens estimados).
type HistBin struct {
	From  int `json:"from"`
	Count int `json:"count"`
}

// ChunkPreview es una muestra corta de un chunk (misma exposición que las
// «fuentes» del chat: la app no tiene modelo de permisos).
type ChunkPreview struct {
	Index   int    `json:"index"`
	Page    int    `json:"page"`
	Tokens  int    `json:"tokens"`
	Snippet string `json:"snippet"`
}

// EventRow es un evento de rag_events serializado para la UI.
type EventRow struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Model        string    `json:"model,omitempty"`
	Status       string    `json:"status"`
	HTTPStatus   int       `json:"httpStatus,omitempty"`
	ErrorKind    string    `json:"errorKind,omitempty"`
	ErrorDetail  string    `json:"errorDetail,omitempty"`
	LatencyMs    int64     `json:"latencyMs"`
	QueueMs      int64     `json:"queueMs,omitempty"`
	Attempts     int       `json:"attempts,omitempty"`
	BatchSize    int       `json:"batchSize,omitempty"`
	PayloadBytes int       `json:"payloadBytes,omitempty"`
	TokensIn     int       `json:"tokensIn,omitempty"`
	TokensOut    int       `json:"tokensOut,omitempty"`
	TokenSource  string    `json:"tokenSource,omitempty"`
	LoadMs       int64     `json:"loadMs,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// DocDetail es la vista completa de un documento para el cajón de detalle.
type DocDetail struct {
	DocRow
	Hash          string         `json:"hash"`
	PagesStored   int            `json:"pagesStored"`
	AvgChunkTokens int           `json:"avgChunkTokens"`
	TimesCited    int            `json:"timesCited"`
	TokenHist     []HistBin      `json:"tokenHist"`
	Chunks        []ChunkPreview `json:"chunks"`
	Events        []EventRow     `json:"events"`
}

// OpsDocumentDetail arma el detalle de un documento. tokenLimit es el
// contexto del modelo de embeddings (para dimensionar el histograma).
func (s *Store) OpsDocumentDetail(ctx context.Context, id []byte, tokenLimit int) (*DocDetail, error) {
	d := &DocDetail{}
	var errMsg, by, models, hash string
	var processed sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT RAWTOHEX(d.document_id), d.file_name, d.file_hash, NVL(d.file_size_bytes, 0),
		       NVL(d.page_count, 0), d.status, NVL(d.error_message, ' '),
		       NVL(d.uploaded_by, ' '), d.uploaded_at, d.processed_at,
		       NVL(c.chunks, 0), NVL(c.embedded, 0), NVL(c.est_tokens, 0),
		       NVL(c.min_t, 0), NVL(c.max_t, 0), NVL(c.models, ' ')
		FROM documents d
		LEFT JOIN (
			SELECT document_id, COUNT(*) chunks, COUNT(embedding) embedded,
			       SUM(NVL(token_count, 0)) est_tokens,
			       MIN(token_count) min_t, MAX(token_count) max_t,
			       LISTAGG(DISTINCT embedding_model, ',') models
			FROM document_chunks GROUP BY document_id
		) c ON c.document_id = d.document_id
		WHERE d.document_id = :1`, id).
		Scan(&d.ID, &d.FileName, &hash, &d.SizeBytes, &d.PageCount, &d.Status,
			&errMsg, &by, &d.UploadedAt, &processed,
			&d.ChunkCount, &d.EmbeddedChunks, &d.EstTokens,
			&d.MinChunkTokens, &d.MaxChunkTokens, &models)
	if err != nil {
		return nil, err
	}
	d.ID = strings.ToLower(d.ID)
	d.Hash = hash
	d.Error = strings.TrimSpace(errMsg)
	d.UploadedBy = strings.TrimSpace(by)
	d.Models = strings.TrimSpace(models)
	d.MissingChunks = d.ChunkCount - d.EmbeddedChunks
	if processed.Valid {
		t := processed.Time
		d.ProcessedAt = &t
		d.DurationSec = int64(t.Sub(d.UploadedAt).Seconds())
	}
	if d.ChunkCount > 0 {
		d.AvgChunkTokens = int(d.EstTokens) / d.ChunkCount
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM document_pages WHERE document_id = :1`, id).Scan(&d.PagesStored); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM rag_retrieved_chunks rc
		JOIN document_chunks c ON c.chunk_id = rc.chunk_id
		WHERE c.document_id = :1`, id).Scan(&d.TimesCited); err != nil {
		return nil, err
	}

	// Histograma de tokens por chunk (estimados), en bins de tokenLimit/16.
	// El ancho de bin va inline (entero del servidor) para que la expresión
	// del SELECT y la del GROUP BY sean idénticas.
	bin := tokenLimit / 16
	if bin < 25 {
		bin = 25
	}
	binExpr := fmt.Sprintf("FLOOR(NVL(token_count, 0) / %[1]d) * %[1]d", bin)
	hrows, err := s.db.QueryContext(ctx, `
		SELECT `+binExpr+`, COUNT(*)
		FROM document_chunks WHERE document_id = :1
		GROUP BY `+binExpr+`
		ORDER BY 1`, id)
	if err != nil {
		return nil, fmt.Errorf("histograma de chunks: %w", err)
	}
	for hrows.Next() {
		var b HistBin
		if err := hrows.Scan(&b.From, &b.Count); err != nil {
			hrows.Close()
			return nil, err
		}
		d.TokenHist = append(d.TokenHist, b)
	}
	hrows.Close()
	if err := hrows.Err(); err != nil {
		return nil, err
	}

	crows, err := s.db.QueryContext(ctx, `
		SELECT chunk_index, NVL(page_number, 0), NVL(token_count, 0),
		       DBMS_LOB.SUBSTR(chunk_text, 240, 1)
		FROM document_chunks WHERE document_id = :1
		ORDER BY chunk_index FETCH FIRST 3 ROWS ONLY`, id)
	if err != nil {
		return nil, fmt.Errorf("muestras de chunks: %w", err)
	}
	for crows.Next() {
		var c ChunkPreview
		if err := crows.Scan(&c.Index, &c.Page, &c.Tokens, &c.Snippet); err != nil {
			crows.Close()
			return nil, err
		}
		d.Chunks = append(d.Chunks, c)
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}

	d.Events, err = s.eventsForRef(ctx, id)
	return d, err
}

// eventsForRef lista los eventos correlacionados con un documento o consulta.
func (s *Store) eventsForRef(ctx context.Context, ref []byte) ([]EventRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT RAWTOHEX(event_id), kind, NVL(model, ' '), status,
		       NVL(http_status, 0), NVL(error_kind, ' '), NVL(error_detail, ' '),
		       NVL(latency_ms, 0), NVL(queue_ms, 0), NVL(attempts, 0), NVL(batch_size, 0),
		       NVL(payload_bytes, 0), NVL(tokens_in, 0), NVL(tokens_out, 0),
		       NVL(token_source, ' '), NVL(load_ms, 0), NVL(detail, ' '), created_at
		FROM rag_events WHERE ref_id = :1
		ORDER BY created_at FETCH FIRST 200 ROWS ONLY`, ref)
	if err != nil {
		return nil, fmt.Errorf("eventos del recurso: %w", err)
	}
	defer rows.Close()
	return scanEventRows(rows)
}

func scanEventRows(rows *sql.Rows) ([]EventRow, error) {
	out := []EventRow{}
	for rows.Next() {
		var e EventRow
		var model, errKind, errDetail, tokSrc, detail string
		if err := rows.Scan(&e.ID, &e.Kind, &model, &e.Status,
			&e.HTTPStatus, &errKind, &errDetail,
			&e.LatencyMs, &e.QueueMs, &e.Attempts, &e.BatchSize,
			&e.PayloadBytes, &e.TokensIn, &e.TokensOut,
			&tokSrc, &e.LoadMs, &detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ID = strings.ToLower(e.ID)
		e.Model = strings.TrimSpace(model)
		e.ErrorKind = strings.TrimSpace(errKind)
		e.ErrorDetail = strings.TrimSpace(errDetail)
		e.TokenSource = strings.TrimSpace(tokSrc)
		e.Detail = strings.TrimSpace(detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── Consultas (tabla y traza) ────────────────────────────────────────────

// QueryFilter filtra/pagina la tabla de consultas RAG.
type QueryFilter struct {
	Hours     int
	Status    string // answered | pending | error
	Feedback  string // up | down
	NoResults bool
	WeakBelow float64 // >0: top score < umbral
	Model     string
	Sort      string
	Desc      bool
	Offset    int
	Limit     int
}

// QueryRow es una fila de la tabla de consultas.
type QueryRow struct {
	ID              string    `json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	Model           string    `json:"model"`
	Preview         string    `json:"preview"`
	Answered        bool      `json:"answered"`
	Status          string    `json:"status"` // answered | pending | error
	Candidates      int       `json:"candidates"`
	UsedInPrompt    int       `json:"usedInPrompt"`
	TopScore        float64   `json:"topScore"`
	AvgScore        float64   `json:"avgScore"`
	CtxTokensEst    int64     `json:"ctxTokensEst"` // fuente: estimated
	PromptTokens    int       `json:"promptTokens"` // fuente: ollama
	CompletionTokens int      `json:"completionTokens"`
	GenLatencyMs    int64     `json:"genLatencyMs"`
	ErrorKind       string    `json:"errorKind,omitempty"`
	Feedback        int       `json:"feedback"`
}

// QueryPage es una página de la tabla de consultas.
type QueryPage struct {
	Total  int        `json:"total"`
	Offset int        `json:"offset"`
	Limit  int        `json:"limit"`
	Rows   []QueryRow `json:"rows"`
}

var querySortCols = map[string]string{
	"created": "q.created_at",
	"latency": "NVL(g.latency_ms, 0)",
	"tokens":  "NVL(g.tokens_in, 0) + NVL(g.tokens_out, 0)",
	"score":   "NVL(r.top_score, 0)",
}

// OpsQueries devuelve la tabla de consultas paginada y filtrada.
func (s *Store) OpsQueries(ctx context.Context, f QueryFilter) (*QueryPage, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 25
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf(":%d", len(args))
	}
	if f.Hours > 0 {
		where = append(where, "q.created_at >= SYSTIMESTAMP - NUMTODSINTERVAL("+arg(rangeHours(f.Hours))+", 'HOUR')")
	}
	switch f.Status {
	case "answered":
		where = append(where, "q.response_text IS NOT NULL")
	case "error":
		where = append(where, "q.response_text IS NULL AND g.status = 'ERROR'")
	case "pending":
		where = append(where, "q.response_text IS NULL AND (g.status IS NULL OR g.status = 'OK')")
	}
	switch f.Feedback {
	case "up":
		where = append(where, "NVL(fb.rating, 0) > 0")
	case "down":
		where = append(where, "NVL(fb.rating, 0) < 0")
	}
	if f.NoResults {
		where = append(where, "NVL(r.cnt, 0) = 0")
	}
	if f.WeakBelow > 0 {
		where = append(where, "NVL(r.cnt, 0) > 0 AND NVL(r.top_score, 0) < "+arg(f.WeakBelow))
	}
	if f.Model != "" {
		where = append(where, "UPPER(NVL(q.llm_model, ' ')) LIKE UPPER("+arg("%"+f.Model+"%")+")")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}

	const fromSQL = `
	FROM rag_queries q
	LEFT JOIN (
		SELECT rc.query_id, COUNT(*) cnt,
		       SUM(CASE WHEN rc.was_used_in_prompt = 'Y' THEN 1 ELSE 0 END) used,
		       MAX(rc.similarity_score) top_score, AVG(rc.similarity_score) avg_score,
		       SUM(NVL(c.token_count, 0)) ctx_tokens
		FROM rag_retrieved_chunks rc
		LEFT JOIN document_chunks c ON c.chunk_id = rc.chunk_id
		GROUP BY rc.query_id
	) r ON r.query_id = q.query_id
	LEFT JOIN (
		SELECT ref_id,
		       MAX(status)      KEEP (DENSE_RANK LAST ORDER BY created_at) status,
		       MAX(latency_ms)  KEEP (DENSE_RANK LAST ORDER BY created_at) latency_ms,
		       MAX(tokens_in)   KEEP (DENSE_RANK LAST ORDER BY created_at) tokens_in,
		       MAX(tokens_out)  KEEP (DENSE_RANK LAST ORDER BY created_at) tokens_out,
		       MAX(error_kind)  KEEP (DENSE_RANK LAST ORDER BY created_at) error_kind
		FROM rag_events WHERE kind = 'generation'
		GROUP BY ref_id
	) g ON g.ref_id = q.query_id
	LEFT JOIN (
		SELECT query_id, MAX(rating) KEEP (DENSE_RANK LAST ORDER BY created_at) rating
		FROM rag_feedback GROUP BY query_id
	) fb ON fb.query_id = q.query_id `

	page := &QueryPage{Offset: f.Offset, Limit: f.Limit, Rows: []QueryRow{}}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*)"+fromSQL+whereSQL, args...).Scan(&page.Total); err != nil {
		return nil, fmt.Errorf("total de consultas: %w", err)
	}

	sortCol, ok := querySortCols[f.Sort]
	if !ok {
		sortCol, f.Desc = querySortCols["created"], true
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	listArgs := append(append([]any{}, args...), f.Offset, f.Limit)
	query := `
	SELECT RAWTOHEX(q.query_id), q.created_at, NVL(q.llm_model, ' '),
	       DBMS_LOB.SUBSTR(q.query_text, 200, 1),
	       CASE WHEN q.response_text IS NULL THEN 0 ELSE 1 END,
	       NVL(r.cnt, 0), NVL(r.used, 0), NVL(r.top_score, 0), NVL(r.avg_score, 0), NVL(r.ctx_tokens, 0),
	       NVL(g.status, ' '), NVL(g.latency_ms, 0), NVL(g.tokens_in, 0), NVL(g.tokens_out, 0),
	       NVL(g.error_kind, ' '), NVL(fb.rating, 0)` +
		fromSQL + whereSQL +
		fmt.Sprintf(" ORDER BY %s %s, q.query_id OFFSET :%d ROWS FETCH NEXT :%d ROWS ONLY",
			sortCol, dir, len(args)+1, len(args)+2)
	rows, err := s.db.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, fmt.Errorf("listar consultas: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r QueryRow
		var model, genStatus, errKind string
		var answered int
		if err := rows.Scan(&r.ID, &r.CreatedAt, &model, &r.Preview, &answered,
			&r.Candidates, &r.UsedInPrompt, &r.TopScore, &r.AvgScore, &r.CtxTokensEst,
			&genStatus, &r.GenLatencyMs, &r.PromptTokens, &r.CompletionTokens,
			&errKind, &r.Feedback); err != nil {
			return nil, err
		}
		r.ID = strings.ToLower(r.ID)
		r.Model = strings.TrimSpace(model)
		r.ErrorKind = strings.TrimSpace(errKind)
		r.Answered = answered == 1
		switch {
		case r.Answered:
			r.Status = "answered"
		case strings.TrimSpace(genStatus) == "ERROR":
			r.Status = "error"
		default:
			r.Status = "pending"
		}
		page.Rows = append(page.Rows, r)
	}
	return page, rows.Err()
}

// TraceChunk es un chunk recuperado dentro de la traza de una consulta.
type TraceChunk struct {
	Rank       int     `json:"rank"`
	Score      float64 `json:"score"`
	Used       bool    `json:"used"`
	ChunkID    string  `json:"chunkId"`
	ChunkIndex int     `json:"chunkIndex"`
	Page       int     `json:"page"`
	Tokens     int     `json:"tokens"` // fuente: estimated
	Snippet    string  `json:"snippet"`
	FileName   string  `json:"fileName"`
	DocumentID string  `json:"documentId"`
	Model      string  `json:"model"`
}

// TraceFeedback es una valoración registrada sobre la respuesta.
type TraceFeedback struct {
	Rating    int       `json:"rating"`
	Comment   string    `json:"comment,omitempty"`
	CreatedBy string    `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// QueryTrace es la traza completa de una consulta RAG.
type QueryTrace struct {
	ID           string          `json:"id"`
	CreatedAt    time.Time       `json:"createdAt"`
	Question     string          `json:"question"`
	Response     string          `json:"response,omitempty"`
	Model        string          `json:"model"`
	SessionID    string          `json:"sessionId,omitempty"`
	UserID       string          `json:"userId,omitempty"`
	Template     string          `json:"template,omitempty"` // nombre vN
	Chunks       []TraceChunk    `json:"chunks"`
	Events       []EventRow      `json:"events"`
	Feedback     []TraceFeedback `json:"feedback"`
}

// OpsQueryTrace arma la traza completa de una consulta.
func (s *Store) OpsQueryTrace(ctx context.Context, id []byte) (*QueryTrace, error) {
	t := &QueryTrace{}
	var model, sess, user, tplName string
	var response sql.NullString
	var tplVersion sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT RAWTOHEX(q.query_id), q.created_at, q.query_text, q.response_text,
		       NVL(q.llm_model, ' '), NVL(RAWTOHEX(q.session_id), ' '), NVL(q.user_id, ' '),
		       NVL(p.name, ' '), p.version
		FROM rag_queries q
		LEFT JOIN prompt_templates p ON p.template_id = q.prompt_template_id
		WHERE q.query_id = :1`, id).
		Scan(&t.ID, &t.CreatedAt, &t.Question, &response, &model, &sess, &user, &tplName, &tplVersion)
	if err != nil {
		return nil, err
	}
	t.ID = strings.ToLower(t.ID)
	t.Model = strings.TrimSpace(model)
	t.SessionID = strings.ToLower(strings.TrimSpace(sess))
	t.UserID = strings.TrimSpace(user)
	if response.Valid {
		t.Response = response.String
	}
	if name := strings.TrimSpace(tplName); name != "" {
		t.Template = name
		if tplVersion.Valid {
			t.Template = fmt.Sprintf("%s v%d", name, tplVersion.Int64)
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT NVL(rc.rank_position, 0), NVL(rc.similarity_score, 0), NVL(rc.was_used_in_prompt, 'N'),
		       RAWTOHEX(c.chunk_id), c.chunk_index, NVL(c.page_number, 0), NVL(c.token_count, 0),
		       DBMS_LOB.SUBSTR(c.chunk_text, 280, 1), d.file_name, RAWTOHEX(d.document_id),
		       NVL(c.embedding_model, ' ')
		FROM rag_retrieved_chunks rc
		JOIN document_chunks c ON c.chunk_id = rc.chunk_id
		JOIN documents d ON d.document_id = c.document_id
		WHERE rc.query_id = :1
		ORDER BY rc.rank_position`, id)
	if err != nil {
		return nil, fmt.Errorf("chunks de la traza: %w", err)
	}
	t.Chunks = []TraceChunk{}
	for rows.Next() {
		var c TraceChunk
		var used, cmodel string
		if err := rows.Scan(&c.Rank, &c.Score, &used, &c.ChunkID, &c.ChunkIndex,
			&c.Page, &c.Tokens, &c.Snippet, &c.FileName, &c.DocumentID, &cmodel); err != nil {
			rows.Close()
			return nil, err
		}
		c.ChunkID = strings.ToLower(c.ChunkID)
		c.DocumentID = strings.ToLower(c.DocumentID)
		c.Used = used == "Y"
		c.Model = strings.TrimSpace(cmodel)
		t.Chunks = append(t.Chunks, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if t.Events, err = s.eventsForRef(ctx, id); err != nil {
		return nil, err
	}

	frows, err := s.db.QueryContext(ctx, `
		SELECT NVL(rating, 0), NVL(feedback_text, ' '), NVL(created_by, ' '), created_at
		FROM rag_feedback WHERE query_id = :1 ORDER BY created_at`, id)
	if err != nil {
		return nil, fmt.Errorf("feedback de la traza: %w", err)
	}
	t.Feedback = []TraceFeedback{}
	for frows.Next() {
		var f TraceFeedback
		var comment, by string
		if err := frows.Scan(&f.Rating, &comment, &by, &f.CreatedAt); err != nil {
			frows.Close()
			return nil, err
		}
		f.Comment = strings.TrimSpace(comment)
		f.CreatedBy = strings.TrimSpace(by)
		t.Feedback = append(t.Feedback, f)
	}
	frows.Close()
	return t, frows.Err()
}

// ── Tokens ───────────────────────────────────────────────────────────────

// TokensReport consolida el uso de tokens por categoría, siempre con fuente.
type TokensReport struct {
	Hours int `json:"hours"`

	Ingest struct {
		ExtractedEst     int64 `json:"extractedEst"`     // Σ token_count de chunks (estimated, global)
		AttemptedEst     int64 `json:"attemptedEst"`     // Σ payload/4 de llamadas embed de ingesta (estimated, rango)
		Successful       int64 `json:"successful"`       // Σ tokens_in OK (ollama, rango)
		AvgChunk         int   `json:"avgChunk"`         // estimated, global
		MinChunk         int   `json:"minChunk"`
		MaxChunk         int   `json:"maxChunk"`
		NearLimit        int   `json:"nearLimit"`        // ≥90 % del contexto
		OverLimit        int   `json:"overLimit"`        // > contexto (no debería existir)
		RejectedInputs   int   `json:"rejectedInputs"`   // validación local (rango)
		InputTooLarge400 int   `json:"inputTooLarge400"` // 400 por tamaño (rango)
	} `json:"ingest"`

	Query struct {
		EmbedTokens     int64 `json:"embedTokens"`     // kind=query_embed (rango)
		RetrievedEst    int64 `json:"retrievedEst"`    // Σ tokens de candidatos (estimated, rango)
		SelectedEst     int64 `json:"selectedEst"`     // Σ tokens usados en prompt (estimated, rango)
	} `json:"query"`

	Generation struct {
		PromptTokens     int64 `json:"promptTokens"`     // ollama (rango)
		CompletionTokens int64 `json:"completionTokens"` // ollama (rango)
		Answers          int   `json:"answers"`
		AvgPerAnswer     int   `json:"avgPerAnswer"`
		MaxPerAnswer     int   `json:"maxPerAnswer"`
		StoppedByLimit   int   `json:"stoppedByLimit"` // done_reason ≠ stop
	} `json:"generation"`

	ByModel []ModelTokens `json:"byModel"`
	ByDoc   []DocTokens   `json:"byDoc"`
}

// ModelTokens agrega tokens por modelo y tipo de operación (fuente: ollama).
type ModelTokens struct {
	Model     string `json:"model"`
	Kind      string `json:"kind"`
	Requests  int    `json:"requests"`
	TokensIn  int64  `json:"tokensIn"`
	TokensOut int64  `json:"tokensOut"`
}

// DocTokens es el peso en tokens estimados de un documento.
type DocTokens struct {
	FileName string `json:"fileName"`
	ID       string `json:"id"`
	Tokens   int64  `json:"tokens"`
	Chunks   int    `json:"chunks"`
}

// OpsTokens consolida las métricas de tokens. tokenLimit = contexto del
// modelo de embeddings.
func (s *Store) OpsTokens(ctx context.Context, hours, tokenLimit int) (*TokensReport, error) {
	hours = rangeHours(hours)
	r := &TokensReport{Hours: hours}

	var avg sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
		SELECT NVL(SUM(token_count), 0), AVG(token_count), NVL(MIN(token_count), 0), NVL(MAX(token_count), 0),
		       NVL(SUM(CASE WHEN token_count >= :1 THEN 1 ELSE 0 END), 0),
		       NVL(SUM(CASE WHEN token_count > :2 THEN 1 ELSE 0 END), 0)
		FROM document_chunks`, tokenLimit*9/10, tokenLimit).
		Scan(&r.Ingest.ExtractedEst, &avg, &r.Ingest.MinChunk, &r.Ingest.MaxChunk,
			&r.Ingest.NearLimit, &r.Ingest.OverLimit); err != nil {
		return nil, fmt.Errorf("tokens de chunks: %w", err)
	}
	r.Ingest.AvgChunk = int(avg.Float64)

	if err := s.db.QueryRowContext(ctx, `
		SELECT NVL(SUM(NVL(payload_bytes, 0)), 0) / 4,
		       NVL(SUM(CASE WHEN status = 'OK' THEN NVL(tokens_in, 0) ELSE 0 END), 0),
		       NVL(SUM(CASE WHEN error_kind = 'validation' THEN 1 ELSE 0 END), 0),
		       NVL(SUM(CASE WHEN error_kind = 'http_400' THEN 1 ELSE 0 END), 0)
		FROM rag_events
		WHERE kind = 'embed' AND detail = 'ingest'
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')`, hours).
		Scan(&r.Ingest.AttemptedEst, &r.Ingest.Successful,
			&r.Ingest.RejectedInputs, &r.Ingest.InputTooLarge400); err != nil {
		return nil, fmt.Errorf("tokens de ingesta: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT NVL(SUM(tokens_in), 0) FROM rag_events
		WHERE kind = 'query_embed' AND status = 'OK'
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')`, hours).
		Scan(&r.Query.EmbedTokens); err != nil {
		return nil, fmt.Errorf("tokens de consulta: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT NVL(SUM(NVL(c.token_count, 0)), 0),
		       NVL(SUM(CASE WHEN rc.was_used_in_prompt = 'Y' THEN NVL(c.token_count, 0) ELSE 0 END), 0)
		FROM rag_retrieved_chunks rc
		JOIN rag_queries q ON q.query_id = rc.query_id
		LEFT JOIN document_chunks c ON c.chunk_id = rc.chunk_id
		WHERE q.created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')`, hours).
		Scan(&r.Query.RetrievedEst, &r.Query.SelectedEst); err != nil {
		return nil, fmt.Errorf("tokens de contexto: %w", err)
	}

	var avgAns sql.NullFloat64
	var maxAns sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT NVL(SUM(tokens_in), 0), NVL(SUM(tokens_out), 0), COUNT(*),
		       AVG(tokens_out), MAX(tokens_out),
		       NVL(SUM(CASE WHEN detail LIKE '%stop=%' THEN 1 ELSE 0 END), 0)
		FROM rag_events
		WHERE kind = 'generation' AND status = 'OK'
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')`, hours).
		Scan(&r.Generation.PromptTokens, &r.Generation.CompletionTokens, &r.Generation.Answers,
			&avgAns, &maxAns, &r.Generation.StoppedByLimit); err != nil {
		return nil, fmt.Errorf("tokens de generación: %w", err)
	}
	r.Generation.AvgPerAnswer = int(avgAns.Float64)
	r.Generation.MaxPerAnswer = int(maxAns.Int64)

	mrows, err := s.db.QueryContext(ctx, `
		SELECT NVL(model, 'desconocido'), kind, COUNT(*),
		       NVL(SUM(tokens_in), 0), NVL(SUM(tokens_out), 0)
		FROM rag_events
		WHERE kind IN ('embed', 'query_embed', 'generation')
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
		GROUP BY model, kind ORDER BY 4 DESC`, hours)
	if err != nil {
		return nil, fmt.Errorf("tokens por modelo: %w", err)
	}
	r.ByModel = []ModelTokens{}
	for mrows.Next() {
		var m ModelTokens
		if err := mrows.Scan(&m.Model, &m.Kind, &m.Requests, &m.TokensIn, &m.TokensOut); err != nil {
			mrows.Close()
			return nil, err
		}
		r.ByModel = append(r.ByModel, m)
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, err
	}

	drows, err := s.db.QueryContext(ctx, `
		SELECT d.file_name, RAWTOHEX(d.document_id), NVL(SUM(c.token_count), 0), COUNT(*)
		FROM document_chunks c
		JOIN documents d ON d.document_id = c.document_id
		GROUP BY d.file_name, d.document_id
		ORDER BY 3 DESC FETCH FIRST 12 ROWS ONLY`)
	if err != nil {
		return nil, fmt.Errorf("tokens por documento: %w", err)
	}
	r.ByDoc = []DocTokens{}
	for drows.Next() {
		var d DocTokens
		if err := drows.Scan(&d.FileName, &d.ID, &d.Tokens, &d.Chunks); err != nil {
			drows.Close()
			return nil, err
		}
		d.ID = strings.ToLower(d.ID)
		r.ByDoc = append(r.ByDoc, d)
	}
	drows.Close()
	return r, drows.Err()
}

// ── Modelos / Ollama ─────────────────────────────────────────────────────

// ModelAgg agrega las llamadas de un modelo dentro del rango.
type ModelAgg struct {
	Model    string  `json:"model"`
	Kind     string  `json:"kind"`
	Requests int     `json:"requests"`
	Failures int     `json:"failures"`
	Retries  int     `json:"retries"`
	AvgMs    float64 `json:"avgMs"`
	P95Ms    float64 `json:"p95Ms"`
}

// ModelOps son las métricas por modelo derivadas de rag_events.
type ModelOps struct {
	Hours         int         `json:"hours"`
	PerModel      []ModelAgg  `json:"perModel"`
	HTTPStatuses  []KindCount `json:"httpStatuses"`
	ErrorKinds    []KindCount `json:"errorKinds"`
	LoadEvents    int         `json:"loadEvents"` // llamadas con (re)carga ≥1 s
	AvgLoadMs     int64       `json:"avgLoadMs"`
	MaxLoadMs     int64       `json:"maxLoadMs"`
	QueueAvgMs    int64       `json:"queueAvgMs"`
	QueueMaxMs    int64       `json:"queueMaxMs"`
	RetryExhausted int        `json:"retryExhausted"` // fallo con todos los intentos gastados
	LastEmbedOK   *time.Time  `json:"lastEmbedOk,omitempty"`
	LastEmbedFail *time.Time  `json:"lastEmbedFail,omitempty"`
	LastEmbedError string     `json:"lastEmbedError,omitempty"`
}

// OpsModelStats agrega la actividad por modelo dentro del rango. maxAttempts
// es el tope de reintentos del cliente (para contar agotamientos).
func (s *Store) OpsModelStats(ctx context.Context, hours, maxAttempts int) (*ModelOps, error) {
	hours = rangeHours(hours)
	m := &ModelOps{Hours: hours, PerModel: []ModelAgg{}, HTTPStatuses: []KindCount{}, ErrorKinds: []KindCount{}}

	rows, err := s.db.QueryContext(ctx, `
		SELECT NVL(model, 'desconocido'), kind, COUNT(*),
		       NVL(SUM(CASE WHEN status = 'ERROR' THEN 1 ELSE 0 END), 0),
		       NVL(SUM(GREATEST(NVL(attempts, 1) - 1, 0)), 0),
		       NVL(AVG(latency_ms), 0),
		       NVL(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)
		FROM rag_events
		WHERE kind IN ('embed', 'query_embed', 'generation')
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
		GROUP BY model, kind ORDER BY 3 DESC`, hours)
	if err != nil {
		return nil, fmt.Errorf("actividad por modelo: %w", err)
	}
	for rows.Next() {
		var a ModelAgg
		if err := rows.Scan(&a.Model, &a.Kind, &a.Requests, &a.Failures, &a.Retries, &a.AvgMs, &a.P95Ms); err != nil {
			rows.Close()
			return nil, err
		}
		m.PerModel = append(m.PerModel, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hrows, err := s.db.QueryContext(ctx, `
		SELECT TO_CHAR(http_status), COUNT(*) FROM rag_events
		WHERE http_status IS NOT NULL AND status = 'ERROR'
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
		GROUP BY http_status ORDER BY 2 DESC`, hours)
	if err != nil {
		return nil, fmt.Errorf("distribución HTTP: %w", err)
	}
	for hrows.Next() {
		var kc KindCount
		if err := hrows.Scan(&kc.Kind, &kc.Count); err != nil {
			hrows.Close()
			return nil, err
		}
		m.HTTPStatuses = append(m.HTTPStatuses, kc)
	}
	hrows.Close()
	if err := hrows.Err(); err != nil {
		return nil, err
	}

	erows, err := s.db.QueryContext(ctx, `
		SELECT NVL(error_kind, 'desconocido'), COUNT(*) FROM rag_events
		WHERE status = 'ERROR' AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:1, 'HOUR')
		GROUP BY error_kind ORDER BY 2 DESC`, hours)
	if err != nil {
		return nil, fmt.Errorf("errores por categoría: %w", err)
	}
	for erows.Next() {
		var kc KindCount
		if err := erows.Scan(&kc.Kind, &kc.Count); err != nil {
			erows.Close()
			return nil, err
		}
		m.ErrorKinds = append(m.ErrorKinds, kc)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return nil, err
	}

	var avgLoad, avgQueue sql.NullFloat64
	var maxLoad, maxQueue sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT NVL(SUM(CASE WHEN NVL(load_ms, 0) >= 1000 THEN 1 ELSE 0 END), 0),
		       AVG(CASE WHEN NVL(load_ms, 0) >= 1000 THEN load_ms END),
		       MAX(load_ms), AVG(queue_ms), MAX(queue_ms),
		       NVL(SUM(CASE WHEN status = 'ERROR' AND NVL(attempts, 0) >= :1 THEN 1 ELSE 0 END), 0)
		FROM rag_events
		WHERE kind IN ('embed', 'query_embed')
		  AND created_at >= SYSTIMESTAMP - NUMTODSINTERVAL(:2, 'HOUR')`, maxAttempts, hours).
		Scan(&m.LoadEvents, &avgLoad, &maxLoad, &avgQueue, &maxQueue, &m.RetryExhausted); err != nil {
		return nil, fmt.Errorf("cargas y colas: %w", err)
	}
	m.AvgLoadMs = int64(avgLoad.Float64)
	m.MaxLoadMs = maxLoad.Int64
	m.QueueAvgMs = int64(avgQueue.Float64)
	m.QueueMaxMs = maxQueue.Int64

	var lastOK, lastFail sql.NullTime
	var lastErr sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT MAX(CASE WHEN status = 'OK' THEN created_at END),
		       MAX(CASE WHEN status = 'ERROR' THEN created_at END)
		FROM rag_events WHERE kind IN ('embed', 'query_embed')`).
		Scan(&lastOK, &lastFail); err != nil {
		return nil, fmt.Errorf("último embedding: %w", err)
	}
	if lastOK.Valid {
		m.LastEmbedOK = &lastOK.Time
	}
	if lastFail.Valid {
		m.LastEmbedFail = &lastFail.Time
		if err := s.db.QueryRowContext(ctx, `
			SELECT error_detail FROM rag_events
			WHERE kind IN ('embed', 'query_embed') AND status = 'ERROR'
			ORDER BY created_at DESC FETCH FIRST 1 ROWS ONLY`).Scan(&lastErr); err == nil && lastErr.Valid {
			m.LastEmbedError = lastErr.String
		}
	}
	return m, nil
}

// LastEmbedStatus devuelve el resultado del embedding más reciente (para la
// salud del modelo sin sondas caras): ok, cuándo, y detalle si falló.
func (s *Store) LastEmbedStatus(ctx context.Context) (ok bool, at time.Time, detail string, err error) {
	return s.lastOpStatus(ctx, "kind IN ('embed', 'query_embed')")
}

// LastGenerationStatus devuelve el resultado de la generación más reciente.
func (s *Store) LastGenerationStatus(ctx context.Context) (ok bool, at time.Time, detail string, err error) {
	return s.lastOpStatus(ctx, "kind = 'generation'")
}

func (s *Store) lastOpStatus(ctx context.Context, kindCond string) (ok bool, at time.Time, detail string, err error) {
	var status string
	var errDetail sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT status, created_at, error_detail FROM rag_events
		WHERE `+kindCond+`
		ORDER BY created_at DESC FETCH FIRST 1 ROWS ONLY`).Scan(&status, &at, &errDetail)
	if err == sql.ErrNoRows {
		return false, time.Time{}, "", nil
	}
	if err != nil {
		return false, time.Time{}, "", err
	}
	return status == "OK", at, errDetail.String, nil
}

// VectorIndexPresent informa si el índice vectorial existe en el esquema.
func (s *Store) VectorIndexPresent(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_indexes WHERE index_name = 'IDX_CHUNKS_EMBEDDING'`).Scan(&n)
	return n > 0, err
}

// IngestionQueue devuelve la profundidad de la cola de ingesta (documentos en
// estados intermedios) y cuántos llevan atascados más de 30 minutos.
func (s *Store) IngestionQueue(ctx context.Context) (depth, stuck int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       NVL(SUM(CASE WHEN uploaded_at < SYSTIMESTAMP - NUMTODSINTERVAL(30, 'MINUTE') THEN 1 ELSE 0 END), 0)
		FROM documents WHERE status IN ('UPLOADED', 'EXTRACTING', 'CHUNKED')`).Scan(&depth, &stuck)
	return depth, stuck, err
}

// ── Integridad ───────────────────────────────────────────────────────────

// IntegrityCheck es el resultado de un chequeo de consistencia.
type IntegrityCheck struct {
	ID      string   `json:"id"`
	Count   int      `json:"count"`
	Samples []string `json:"samples"`
	Err     string   `json:"error,omitempty"`
}

// OpsIntegrity ejecuta los chequeos de consistencia del corpus. Cada chequeo
// es independiente: si uno falla (p.ej. una función no disponible) se reporta
// su error sin tumbar el resto. Solo lectura: nunca repara ni borra.
func (s *Store) OpsIntegrity(ctx context.Context, activeModel string, tokenLimit int) []IntegrityCheck {
	type check struct {
		id   string
		sql  string
		args []any
	}
	checks := []check{
		{"docs_embedded_sin_chunks", `
			SELECT d.file_name, COUNT(*) OVER () FROM documents d
			WHERE d.status = 'EMBEDDED'
			  AND NOT EXISTS (SELECT 1 FROM document_chunks c WHERE c.document_id = d.document_id)
			FETCH FIRST 10 ROWS ONLY`, nil},
		{"chunks_sin_embedding", `
			SELECT d.file_name || ' #' || c.chunk_index, COUNT(*) OVER ()
			FROM document_chunks c JOIN documents d ON d.document_id = c.document_id
			WHERE c.embedding IS NULL
			FETCH FIRST 10 ROWS ONLY`, nil},
		{"chunks_vacios", `
			SELECT d.file_name || ' #' || c.chunk_index, COUNT(*) OVER ()
			FROM document_chunks c JOIN documents d ON d.document_id = c.document_id
			WHERE NVL(DBMS_LOB.GETLENGTH(c.chunk_text), 0) = 0
			FETCH FIRST 10 ROWS ONLY`, nil},
		{"chunks_sobre_limite", `
			SELECT d.file_name || ' #' || c.chunk_index || ' (~' || c.token_count || ' tokens)', COUNT(*) OVER ()
			FROM document_chunks c JOIN documents d ON d.document_id = c.document_id
			WHERE c.token_count > :1
			FETCH FIRST 10 ROWS ONLY`, []any{tokenLimit}},
		{"docs_modelos_mixtos", `
			SELECT file_name, COUNT(*) OVER () FROM documents WHERE document_id IN (
				SELECT document_id FROM document_chunks
				WHERE embedding_model IS NOT NULL
				GROUP BY document_id HAVING COUNT(DISTINCT embedding_model) > 1)
			FETCH FIRST 10 ROWS ONLY`, nil},
		{"chunks_modelo_inactivo", `
			SELECT d.file_name || ' → ' || c.embedding_model, COUNT(*) OVER ()
			FROM document_chunks c JOIN documents d ON d.document_id = c.document_id
			WHERE c.embedding_model IS NOT NULL AND c.embedding_model <> :1
			FETCH FIRST 10 ROWS ONLY`, []any{activeModel}},
		{"docs_failed_con_avance", `
			SELECT d.file_name || ' (' || cnt.n || ' chunks listos)', COUNT(*) OVER ()
			FROM documents d
			JOIN (SELECT document_id, COUNT(embedding) n FROM document_chunks GROUP BY document_id) cnt
			  ON cnt.document_id = d.document_id
			WHERE d.status = 'FAILED' AND cnt.n > 0
			FETCH FIRST 10 ROWS ONLY`, nil},
		{"docs_atascados", `
			SELECT file_name || ' (' || status || ')', COUNT(*) OVER () FROM documents
			WHERE status IN ('UPLOADED', 'EXTRACTING', 'CHUNKED')
			  AND uploaded_at < SYSTIMESTAMP - NUMTODSINTERVAL(30, 'MINUTE')
			FETCH FIRST 10 ROWS ONLY`, nil},
		{"consultas_sin_respuesta", `
			SELECT RAWTOHEX(query_id), COUNT(*) OVER () FROM rag_queries
			WHERE response_text IS NULL AND created_at < SYSTIMESTAMP - NUMTODSINTERVAL(10, 'MINUTE')
			FETCH FIRST 10 ROWS ONLY`, nil},
		{"evidencia_de_docs_no_indexados", `
			SELECT DISTINCT d.file_name || ' (' || d.status || ')', COUNT(*) OVER ()
			FROM rag_retrieved_chunks rc
			JOIN document_chunks c ON c.chunk_id = rc.chunk_id
			JOIN documents d ON d.document_id = c.document_id
			WHERE d.status <> 'EMBEDDED'
			FETCH FIRST 10 ROWS ONLY`, nil},
		{"vectores_norma_cero", `
			SELECT d.file_name || ' #' || c.chunk_index, COUNT(*) OVER ()
			FROM document_chunks c JOIN documents d ON d.document_id = c.document_id
			WHERE c.embedding IS NOT NULL AND VECTOR_NORM(c.embedding) < 0.000001
			FETCH FIRST 10 ROWS ONLY`, nil},
	}

	out := make([]IntegrityCheck, 0, len(checks))
	for _, c := range checks {
		ic := IntegrityCheck{ID: c.id, Samples: []string{}}
		rows, err := s.db.QueryContext(ctx, c.sql, c.args...)
		if err != nil {
			ic.Err = err.Error()
			out = append(out, ic)
			continue
		}
		for rows.Next() {
			var label string
			var total int
			if err := rows.Scan(&label, &total); err != nil {
				ic.Err = err.Error()
				break
			}
			ic.Count = total
			ic.Samples = append(ic.Samples, label)
		}
		if err := rows.Err(); err != nil && ic.Err == "" {
			ic.Err = err.Error()
		}
		rows.Close()
		out = append(out, ic)
	}
	return out
}
