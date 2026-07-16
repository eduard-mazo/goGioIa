package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamConcurrencyLimit comprueba que con maxConcurrent=1 la segunda
// generación no llega al servidor hasta que se cierra el stream de la primera.
func TestStreamConcurrencyLimit(t *testing.T) {
	entered := make(chan struct{}, 4)
	proceed := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // el cliente recibe cabeceras y Stream retorna
		<-proceed
	}))
	defer srv.Close()
	defer close(proceed)

	c := New(srv.URL+"/api/chat", 1)
	ctx := context.Background()
	msgs := []Message{{Role: "user", Content: "hola"}}

	body1, err := c.Stream(ctx, "m", msgs, nil)
	if err != nil {
		t.Fatalf("primer Stream: %v", err)
	}
	<-entered // la primera petición está dentro

	second := make(chan error, 1)
	go func() {
		body2, err := c.Stream(ctx, "m", msgs, nil)
		if err == nil {
			body2.Close()
		}
		second <- err
	}()

	// Con el hueco ocupado, la segunda no debe entrar al servidor.
	select {
	case <-entered:
		t.Fatal("la segunda generación entró con el semáforo lleno")
	case <-time.After(150 * time.Millisecond):
	}

	// Cerrar el primer stream libera el hueco y la segunda avanza.
	body1.Close()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("la segunda generación no entró tras liberar el semáforo")
	}
	if err := <-second; err != nil {
		t.Fatalf("segundo Stream: %v", err)
	}
}

// TestStreamAcquireCancelled: esperar el semáforo respeta la cancelación.
func TestStreamAcquireCancelled(t *testing.T) {
	c := New("http://127.0.0.1:0/api/chat", 1)
	c.sem <- struct{}{} // ocupar el único hueco sin red de por medio

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Stream(ctx, "m", []Message{{Role: "user", Content: "x"}}, nil)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("se esperaba error por cancelación, err=%v", err)
	}
}

// TestReleaseCloserIdempotent: cerrar dos veces libera el hueco una sola vez.
func TestReleaseCloserIdempotent(t *testing.T) {
	c := New("http://127.0.0.1:0/api/chat", 1)
	release, err := c.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	release() // segunda llamada: no debe des-balancear el canal

	if len(c.sem) != 0 {
		t.Fatalf("el semáforo quedó con %d ocupaciones tras liberar", len(c.sem))
	}
	// El hueco debe poder adquirirse de nuevo sin bloqueo.
	if _, err := c.acquire(context.Background()); err != nil {
		t.Fatalf("re-adquirir tras liberar: %v", err)
	}
}

// TestEmbedRespectsSemaphore: los embeddings comparten el semáforo con la
// generación; con el hueco ocupado por un stream, Embed no llega al servidor
// hasta que el stream se cierra.
func TestEmbedRespectsSemaphore(t *testing.T) {
	streamIn := make(chan struct{}, 1)
	embedIn := make(chan struct{}, 1)
	proceed := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/embed") {
			embedIn <- struct{}{}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2]]}`))
			return
		}
		streamIn <- struct{}{}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-proceed
	}))
	defer srv.Close()
	defer close(proceed)

	c := New(srv.URL+"/api/chat", 1)
	ctx := context.Background()

	body, err := c.Stream(ctx, "m", []Message{{Role: "user", Content: "hola"}}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	<-streamIn

	done := make(chan error, 1)
	go func() {
		_, err := c.EmbedOne(ctx, "m", "texto")
		done <- err
	}()

	select {
	case <-embedIn:
		t.Fatal("Embed entró al servidor con el semáforo lleno")
	case <-time.After(150 * time.Millisecond):
	}

	body.Close()
	select {
	case <-embedIn:
	case <-time.After(2 * time.Second):
		t.Fatal("Embed no entró tras liberar el semáforo")
	}
	if err := <-done; err != nil {
		t.Fatalf("EmbedOne: %v", err)
	}
}
