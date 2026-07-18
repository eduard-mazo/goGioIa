package ollama

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetries acelera el backoff durante el test y lo restaura al terminar.
func fastRetries(t *testing.T) {
	t.Helper()
	oldBase, oldMax := retryBaseDelay, retryMaxDelay
	retryBaseDelay, retryMaxDelay = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { retryBaseDelay, retryMaxDelay = oldBase, oldMax })
}

// embedOK responde un 200 válido con un vector por entrada.
func embedOK(w http.ResponseWriter, n int) {
	vecs := make([]string, n)
	for i := range vecs {
		vecs[i] = "[0.1,0.2]"
	}
	fmt.Fprintf(w, `{"embeddings":[%s]}`, strings.Join(vecs, ","))
}

func TestEmbedRejectsOversizedInput(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		embedOK(w, 1)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/chat", WithEmbedMaxTokens(2048))
	_, err := c.Embed(context.Background(), "m", []string{strings.Repeat("a", 2048*conservativeCharsPerToken+1)})
	if _, ok := errors.AsType[*ValidationError](err); !ok {
		t.Fatalf("esperaba ValidationError, obtuve %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("no debería haber llamado a Ollama, hubo %d llamadas", n)
	}
}

func TestEmbedRejectsEmptyInput(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		embedOK(w, 1)
	}))
	defer srv.Close()

	c := New(srv.URL + "/api/chat")
	if _, err := c.Embed(context.Background(), "m", []string{"   "}); err == nil {
		t.Fatal("esperaba error para entrada vacía")
	} else if _, ok := errors.AsType[*ValidationError](err); !ok {
		t.Fatalf("esperaba ValidationError, obtuve %v", err)
	}
	// Lote vacío: sin error y sin llamadas.
	if vecs, err := c.Embed(context.Background(), "m", nil); err != nil || vecs != nil {
		t.Fatalf("lote vacío: esperaba (nil, nil), obtuve (%v, %v)", vecs, err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("no debería haber llamado a Ollama, hubo %d llamadas", n)
	}
}

func TestEmbedBadRequestNoRetryAndKeepsBody(t *testing.T) {
	fastRetries(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid options: unknown_flag"}`)
	}))
	defer srv.Close()

	c := New(srv.URL + "/api/chat")
	_, err := c.Embed(context.Background(), "m", []string{"hola"})
	httpErr, ok := errors.AsType[*HTTPError](err)
	if !ok {
		t.Fatalf("esperaba *HTTPError, obtuve %v", err)
	}
	if httpErr.StatusCode != http.StatusBadRequest || !strings.Contains(httpErr.Body, "unknown_flag") {
		t.Fatalf("el error no conserva el cuerpo saneado: %+v", httpErr)
	}
	if httpErr.IsInputTooLarge() {
		t.Fatal("un 400 ajeno al tamaño no debe clasificarse como oversize")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("un 400 no se reintenta: esperaba 1 llamada, hubo %d", n)
	}
}

func TestHTTPErrorInputTooLarge(t *testing.T) {
	e := &HTTPError{StatusCode: 400, Body: "input length exceeds maximum context length"}
	if !e.IsInputTooLarge() {
		t.Fatal("debería reconocer un 400 por tamaño de entrada")
	}
	if (&HTTPError{StatusCode: 500, Body: "context length"}).IsInputTooLarge() {
		t.Fatal("solo un 400 puede ser oversize")
	}
}

func TestEmbedRetriesAfterConnectionReset(t *testing.T) {
	fastRetries(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Simula un reset: se corta la conexión sin responder.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			conn.Close()
			return
		}
		embedOK(w, 1)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/chat", WithMaxAttempts(3))
	vecs, err := c.Embed(context.Background(), "m", []string{"hola"})
	if err != nil {
		t.Fatalf("esperaba recuperación tras el reset: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 2 {
		t.Fatalf("vectores inesperados: %v", vecs)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("esperaba 2 intentos (reset + éxito), hubo %d", n)
	}
}

func TestEmbedRetriesExhausted(t *testing.T) {
	fastRetries(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/chat", WithMaxAttempts(3))
	_, err := c.Embed(context.Background(), "m", []string{"hola"})
	if err == nil || !strings.Contains(err.Error(), "agotados 3 intentos") {
		t.Fatalf("esperaba error de intentos agotados, obtuve %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("esperaba exactamente 3 intentos, hubo %d", n)
	}
}

func TestEmbedRetriesOn503(t *testing.T) {
	fastRetries(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "loading model", http.StatusServiceUnavailable)
			return
		}
		embedOK(w, 1)
	}))
	defer srv.Close()

	c := New(srv.URL + "/api/chat")
	if _, err := c.Embed(context.Background(), "m", []string{"hola"}); err != nil {
		t.Fatalf("esperaba recuperación tras el 503: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("esperaba 2 intentos, hubo %d", n)
	}
}

func TestEmbedConcurrencyLimitedToOne(t *testing.T) {
	var mu sync.Mutex
	cur, peak := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
		embedOK(w, 1)
	}))
	defer srv.Close()

	c := New(srv.URL + "/api/chat") // concurrencia por defecto: 1
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := c.Embed(context.Background(), "m", []string{"hola"}); err != nil {
				t.Errorf("embed: %v", err)
			}
		})
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak != 1 {
		t.Fatalf("esperaba como máximo 1 llamada simultánea, hubo %d", peak)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{&ValidationError{Reason: "x"}, "validation"},
		{&HTTPError{StatusCode: 400, Body: "input length"}, "http_400"},
		{&HTTPError{StatusCode: 429}, "http_429"},
		{&HTTPError{StatusCode: 503}, "http_5xx"},
		{fmt.Errorf("wrap: %w", &HTTPError{StatusCode: 404}), "http_404"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{errors.New("read tcp 1.2.3.4: connection reset by peer"), "connection_reset"},
		{errors.New("algo raro"), "other"},
	}
	for _, c := range cases {
		if got := ClassifyError(c.err); got != c.want {
			t.Errorf("ClassifyError(%v) = %q, esperaba %q", c.err, got, c.want)
		}
	}
}

func TestEmbedEmitsRecorderEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"embeddings":[[0.1,0.2]],"prompt_eval_count":42,"load_duration":2000000000}`)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var events []Event
	c := New(srv.URL+"/api/chat", WithRecorder(func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}))

	ref := []byte{1, 2, 3}
	ctx := WithPurpose(WithRef(context.Background(), ref), "ingest")
	if _, err := c.Embed(ctx, "m", []string{"hola"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("esperaba 1 evento, hubo %d", len(events))
	}
	ev := events[0]
	if !ev.OK || ev.Op != "embed" || ev.Purpose != "ingest" {
		t.Fatalf("evento inesperado: %+v", ev)
	}
	if string(ev.Ref) != string(ref) {
		t.Fatalf("ref no propagada: %v", ev.Ref)
	}
	if ev.TokensIn != 42 {
		t.Fatalf("tokens autoritativos no capturados: %d", ev.TokensIn)
	}
	if ev.LoadDuration != 2*time.Second {
		t.Fatalf("load_duration no capturado: %v", ev.LoadDuration)
	}
	if ev.Attempts != 1 || ev.BatchSize != 1 {
		t.Fatalf("metadatos de llamada incorrectos: %+v", ev)
	}
}

func TestEmbedEmitsErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "input length exceeds maximum context length", http.StatusBadRequest)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var events []Event
	c := New(srv.URL+"/api/chat", WithRecorder(func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}))

	if _, err := c.Embed(context.Background(), "m", []string{"hola"}); err == nil {
		t.Fatal("esperaba error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("esperaba 1 evento, hubo %d", len(events))
	}
	ev := events[0]
	if ev.OK || ev.ErrorKind != "http_400" || ev.HTTPStatus != 400 {
		t.Fatalf("evento de error inesperado: %+v", ev)
	}
}
