-- 0002: seeds — catálogo de roles y configuración base de la aplicación.
-- Idempotente (ON CONFLICT DO NOTHING): re-ejecutable sin efectos.
-- El usuario admin inicial NO se siembra aquí: lo crea el backend en el
-- primer arranque (necesita un hash argon2id generado en runtime).

INSERT INTO app.roles (id, name, description) VALUES
  (1,'admin','Administra configuración, usuarios, sesiones y métricas globales'),
  (2,'user','Usa el chat y consulta sus propias métricas')
ON CONFLICT (id) DO NOTHING;

INSERT INTO app.app_settings (key,value,value_type,is_secret,category,description,validation) VALUES
 -- Ollama
 ('ollama.chat_url','http://10.14.16.193:9091/api/chat','url',false,'ollama','Endpoint de chat de Ollama',NULL),
 ('ollama.default_model','llama3.1:latest','string',false,'ollama','Modelo por defecto',NULL),
 ('ollama.embed_model','nomic-embed-text','string',false,'ollama','Modelo de embeddings (768 dims)',NULL),
 -- Gateway de concurrencia
 ('gateway.max_global','2','int',false,'gateway','Generaciones simultáneas máximas hacia Ollama','min:1,max:16'),
 ('gateway.max_per_user','1','int',false,'gateway','Generaciones simultáneas por usuario','min:1,max:4'),
 ('gateway.queue_size','10','int',false,'gateway','Tamaño máximo de la cola de espera','min:0,max:100'),
 ('gateway.queue_wait_timeout_s','30','int',false,'gateway','Segundos máximos de espera en cola','min:5,max:300'),
 ('gateway.request_timeout_s','300','int',false,'gateway','Timeout total de generación (s)','min:30,max:1800'),
 -- Sesiones y seguridad
 ('session.absolute_ttl_h','12','int',false,'session','Vida máxima de sesión (horas)','min:1,max:72'),
 ('session.idle_ttl_min','30','int',false,'session','Expiración por inactividad (min)','min:5,max:480'),
 ('security.lockout_threshold','5','int',false,'security','Fallos de login antes de bloqueo','min:3,max:20'),
 ('security.mfa_required','true','bool',false,'security','Exigir MFA a todos los usuarios',NULL),
 -- RAG
 ('rag.top_k','5','int',false,'rag','Chunks recuperados por pregunta','min:1,max:20'),
 ('rag.model','mistral:latest','string',false,'rag','Modelo LLM del asistente RAG',NULL),
 -- Oracle 23ai (la contraseña se define cifrada desde el panel en la fase de
 -- configuración; mientras esté vacía el backend usa ORACLE_PASSWORD del entorno)
 ('oracle.host','10.14.16.193','string',false,'oracle','Host Oracle 23ai',NULL),
 ('oracle.port','1521','int',false,'oracle','Puerto Oracle','min:1,max:65535'),
 ('oracle.sid','orcl','string',false,'oracle','SID Oracle',NULL),
 ('oracle.user','useria','string',false,'oracle','Usuario Oracle',NULL),
 ('oracle.password','','string',true,'oracle','Password Oracle (cifrado en BD)',NULL),
 -- Monitoreo de consumo
 ('usage.daily_alert_tokens','2000000','int',false,'usage','Umbral de alerta de tokens/día','min:0,max:1000000000')
ON CONFLICT (key) DO NOTHING;
