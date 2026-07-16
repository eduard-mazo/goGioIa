package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Conversation es la vista de la tabla conversations que consume la API.
type Conversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Mode      string    `json:"mode"`
	Model     string    `json:"model,omitempty"`
	Messages  int       `json:"messages"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ConvMessage es un mensaje persistido de una conversación.
type ConvMessage struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	QueryID   string    `json:"queryId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// CreateConversation registra una conversación y devuelve su id.
func (s *Store) CreateConversation(ctx context.Context, sessionID []byte, userID, title, mode, model string) ([]byte, error) {
	id := newID()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO conversations (conversation_id, session_id, user_id, title, conv_mode, llm_model)
		VALUES (:1, :2, :3, :4, :5, :6)`,
		id, sessionID, nullable(userID), nullable(title), mode, nullable(model))
	if err != nil {
		return nil, err
	}
	return id, nil
}

// ListConversations devuelve las conversaciones de una sesión, la más
// reciente primero.
func (s *Store) ListConversations(ctx context.Context, sessionID []byte) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT RAWTOHEX(c.conversation_id), NVL(c.title, ' '), c.conv_mode, NVL(c.llm_model, ' '),
		       c.created_at, c.updated_at,
		       (SELECT COUNT(*) FROM conversation_messages m WHERE m.conversation_id = c.conversation_id)
		FROM conversations c
		WHERE c.session_id = :1
		ORDER BY c.updated_at DESC
		FETCH FIRST 100 ROWS ONLY`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.Mode, &c.Model,
			&c.CreatedAt, &c.UpdatedAt, &c.Messages); err != nil {
			return nil, err
		}
		c.ID = strings.ToLower(c.ID)
		c.Title = strings.TrimSpace(c.Title)
		c.Model = strings.TrimSpace(c.Model)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConversationMessages devuelve los últimos `limit` mensajes en orden
// cronológico (limit <= 0: todos, con tope de 500).
func (s *Store) ConversationMessages(ctx context.Context, convID []byte, limit int) ([]ConvMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT RAWTOHEX(message_id), role, content, NVL(RAWTOHEX(query_id), ' '), created_at
		FROM conversation_messages
		WHERE conversation_id = :1
		ORDER BY seq DESC
		FETCH FIRST :2 ROWS ONLY`, convID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ConvMessage{}
	for rows.Next() {
		var m ConvMessage
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.QueryID, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ID = strings.ToLower(m.ID)
		m.QueryID = strings.ToLower(strings.TrimSpace(m.QueryID))
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Se consultó descendente para tomar los últimos; se devuelve ascendente.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// AppendMessage añade un mensaje al final de la conversación y actualiza su
// updated_at. El seq se asigna con MAX(seq)+1; ante una colisión (dos envíos
// simultáneos) se reintenta.
func (s *Store) AppendMessage(ctx context.Context, convID []byte, role, content string, queryID []byte) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO conversation_messages (message_id, conversation_id, seq, role, content, query_id)
			SELECT :1, :2, NVL(MAX(seq), 0) + 1, :3, :4, :5
			FROM conversation_messages WHERE conversation_id = :6`,
			newID(), convID, role, clob(content), queryID, convID)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "ORA-00001") {
			return err
		}
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE conversations SET updated_at = SYSTIMESTAMP WHERE conversation_id = :1`, convID)
	return err
}

// ConversationContext devuelve el resumen rodante y los últimos `limit`
// mensajes posteriores a lo ya resumido, en orden cronológico: juntos forman
// el contexto acotado de una conversación arbitrariamente larga.
func (s *Store) ConversationContext(ctx context.Context, convID []byte, limit int) (summary string, msgs []ConvMessage, err error) {
	var upto int
	err = s.db.QueryRowContext(ctx, `
		SELECT NVL(summary, ' '), summary_upto FROM conversations
		WHERE conversation_id = :1`, convID).Scan(&summary, &upto)
	if err != nil {
		return "", nil, err
	}
	summary = strings.TrimSpace(summary)

	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT RAWTOHEX(message_id), role, content, NVL(RAWTOHEX(query_id), ' '), created_at
		FROM conversation_messages
		WHERE conversation_id = :1 AND seq > :2
		ORDER BY seq DESC
		FETCH FIRST :3 ROWS ONLY`, convID, upto, limit)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	msgs, err = scanConvMessages(rows)
	if err != nil {
		return "", nil, err
	}
	reverseMessages(msgs)
	return summary, msgs, nil
}

// SummaryState devuelve el resumen actual, hasta qué seq cubre y el último
// seq de la conversación (para decidir si toca resumir).
func (s *Store) SummaryState(ctx context.Context, convID []byte) (summary string, upto, maxSeq int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT NVL(c.summary, ' '), c.summary_upto,
		       (SELECT NVL(MAX(m.seq), 0) FROM conversation_messages m
		        WHERE m.conversation_id = c.conversation_id)
		FROM conversations c
		WHERE c.conversation_id = :1`, convID).Scan(&summary, &upto, &maxSeq)
	if err != nil {
		return "", 0, 0, err
	}
	return strings.TrimSpace(summary), upto, maxSeq, nil
}

// MessagesBetween devuelve los mensajes con fromSeq < seq <= toSeq en orden.
func (s *Store) MessagesBetween(ctx context.Context, convID []byte, fromSeq, toSeq int) ([]ConvMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT RAWTOHEX(message_id), role, content, NVL(RAWTOHEX(query_id), ' '), created_at
		FROM conversation_messages
		WHERE conversation_id = :1 AND seq > :2 AND seq <= :3
		ORDER BY seq
		FETCH FIRST 200 ROWS ONLY`, convID, fromSeq, toSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConvMessages(rows)
}

// SetSummary guarda el resumen rodante y hasta qué seq cubre.
func (s *Store) SetSummary(ctx context.Context, convID []byte, summary string, upto int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversations SET summary = :1, summary_upto = :2
		WHERE conversation_id = :3`,
		clob(summary), upto, convID)
	return err
}

// scanConvMessages materializa filas de conversation_messages.
func scanConvMessages(rows *sql.Rows) ([]ConvMessage, error) {
	out := []ConvMessage{}
	for rows.Next() {
		var m ConvMessage
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.QueryID, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ID = strings.ToLower(m.ID)
		m.QueryID = strings.ToLower(strings.TrimSpace(m.QueryID))
		out = append(out, m)
	}
	return out, rows.Err()
}

func reverseMessages(msgs []ConvMessage) {
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
}

// DeleteConversation elimina la conversación y sus mensajes (CASCADE).
func (s *Store) DeleteConversation(ctx context.Context, id []byte) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE conversation_id = :1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
