-- 0001: esquema base de la plataforma (auth, sesiones, MFA, configuración,
-- trazabilidad de IA, uso de tokens y auditoría). PostgreSQL >= 16.
-- Sin extensiones: gen_random_uuid() es nativo y el email usa unique en lower().

CREATE SCHEMA IF NOT EXISTS app;

-- ============ ROLES Y USUARIOS ============
CREATE TABLE app.roles (
  id          smallint PRIMARY KEY,
  name        text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT ''
);

CREATE TABLE app.users (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email               text NOT NULL,
  display_name        text NOT NULL,
  password_hash       text NOT NULL,               -- formato PHC argon2id
  role_id             smallint NOT NULL REFERENCES app.roles(id),
  status              text NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active','disabled','pending')),
  must_change_password boolean NOT NULL DEFAULT false,
  failed_login_count  int  NOT NULL DEFAULT 0,
  locked_until        timestamptz,
  password_changed_at timestamptz NOT NULL DEFAULT now(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_users_email ON app.users (lower(email));
CREATE INDEX idx_users_status ON app.users(status);

-- ============ SESIONES ============
CREATE TABLE app.sessions (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id       uuid NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
  token_hash    bytea NOT NULL UNIQUE,        -- sha256 del token opaco
  kind          text NOT NULL DEFAULT 'full'
                  CHECK (kind IN ('full','mfa_pending')),
  ip            inet,
  user_agent    text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  revoked_at    timestamptz,
  revoked_by    uuid REFERENCES app.users(id),
  revoke_reason text
);
CREATE INDEX idx_sessions_user_active ON app.sessions(user_id)
  WHERE revoked_at IS NULL;
CREATE INDEX idx_sessions_expires ON app.sessions(expires_at);

-- ============ MFA ============
CREATE TABLE app.mfa_totp (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id          uuid NOT NULL UNIQUE REFERENCES app.users(id) ON DELETE CASCADE,
  secret_enc       text NOT NULL,             -- 'enc:v1:...' AES-256-GCM
  status           text NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','active','revoked')),
  last_used_step   bigint NOT NULL DEFAULT 0, -- anti-replay TOTP
  enrolled_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app.recovery_codes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id     uuid NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
  code_hash   text NOT NULL,                  -- argon2id
  used_at     timestamptz,
  used_ip     inet,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_recovery_user_unused ON app.recovery_codes(user_id)
  WHERE used_at IS NULL;

-- ============ CONFIGURACIÓN ============
CREATE TABLE app.app_settings (
  key          text PRIMARY KEY,
  value        text NOT NULL,                 -- texto plano o 'enc:v1:...'
  value_type   text NOT NULL CHECK (value_type IN ('string','int','bool','float','json','url')),
  is_secret    boolean NOT NULL DEFAULT false,
  category     text NOT NULL,
  description  text NOT NULL DEFAULT '',
  validation   text,                          -- regex o rango 'min:1,max:64'
  version      int  NOT NULL DEFAULT 1,
  updated_by   uuid REFERENCES app.users(id),
  updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app.app_settings_history (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  key         text NOT NULL,
  old_value   text,                           -- '<encrypted>' si is_secret
  new_value   text NOT NULL,
  version     int NOT NULL,
  changed_by  uuid REFERENCES app.users(id),
  changed_at  timestamptz NOT NULL DEFAULT now(),
  request_id  uuid
);
CREATE INDEX idx_settings_hist_key ON app.app_settings_history(key, changed_at DESC);

-- ============ TRAZABILIDAD DE REQUESTS A OLLAMA ============
-- Particionada por mes; las particiones las crea/asegura el backend
-- (pgstore.ensurePartitions) para el mes corriente y el siguiente.
CREATE TABLE app.ai_requests (
  id              uuid NOT NULL,              -- request_id generado en el gateway
  user_id         uuid NOT NULL REFERENCES app.users(id),
  session_id      uuid,
  conversation_id uuid,
  endpoint        text NOT NULL,              -- '/api/chat' | '/api/rag/ask'
  model           text NOT NULL,
  status          text NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued','running','completed','failed','cancelled','rejected')),
  priority        smallint NOT NULL DEFAULT 0,
  queued_at       timestamptz NOT NULL DEFAULT now(),
  started_at      timestamptz,
  finished_at     timestamptz,
  queue_ms        int,
  duration_ms     int,
  error_detail    text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX idx_ai_req_user_time ON app.ai_requests(user_id, created_at DESC);
CREATE INDEX idx_ai_req_status ON app.ai_requests(status) WHERE status IN ('queued','running');

-- ============ USO DE TOKENS ============
CREATE TABLE app.token_usage (
  id               bigint GENERATED ALWAYS AS IDENTITY,
  request_id       uuid NOT NULL,
  user_id          uuid NOT NULL,
  model            text NOT NULL,
  prompt_tokens    int  NOT NULL DEFAULT 0,
  output_tokens    int  NOT NULL DEFAULT 0,
  estimated        boolean NOT NULL DEFAULT false,
  eval_duration_ms bigint,
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX idx_tokens_user_time ON app.token_usage(user_id, created_at DESC);
CREATE INDEX idx_tokens_model_time ON app.token_usage(model, created_at DESC);

CREATE TABLE app.token_usage_daily (
  day             date NOT NULL,
  user_id         uuid NOT NULL,
  model           text NOT NULL,
  prompt_tokens   bigint NOT NULL DEFAULT 0,
  output_tokens   bigint NOT NULL DEFAULT 0,
  request_count   int    NOT NULL DEFAULT 0,
  estimated_count int    NOT NULL DEFAULT 0,
  PRIMARY KEY (day, user_id, model)
);

-- ============ AUDITORÍA (append-only) ============
CREATE TABLE app.audit_events (
  id          bigint GENERATED ALWAYS AS IDENTITY,
  event_type  text NOT NULL,
  severity    text NOT NULL DEFAULT 'info' CHECK (severity IN ('info','warn','critical')),
  actor_id    uuid,
  target_id   uuid,
  ip          inet,
  user_agent  text,
  request_id  uuid,
  detail      jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX idx_audit_type_time ON app.audit_events(event_type, created_at DESC);
CREATE INDEX idx_audit_actor ON app.audit_events(actor_id, created_at DESC);
