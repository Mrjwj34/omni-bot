CREATE TABLE IF NOT EXISTS timeline_messages (
    seq_id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id TEXT NOT NULL DEFAULT '',
    room_id TEXT NOT NULL,
    msg_id TEXT NOT NULL DEFAULT '',
    sender_id TEXT NOT NULL,
    sender_name TEXT NOT NULL,
    is_bot INTEGER NOT NULL DEFAULT 0,
    is_at_bot INTEGER NOT NULL DEFAULT 0,
    timestamp_ms INTEGER NOT NULL,
    content_json TEXT NOT NULL DEFAULT '[]',
    rendered_text TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'UNPROCESSED',
    archived_to_memory INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_timeline_room_seq ON timeline_messages(room_id, seq_id);
CREATE INDEX IF NOT EXISTS idx_timeline_room_status ON timeline_messages(room_id, status, seq_id);
CREATE INDEX IF NOT EXISTS idx_timeline_room_archived ON timeline_messages(room_id, archived_to_memory, seq_id);
CREATE INDEX IF NOT EXISTS idx_timeline_room_msgid ON timeline_messages(room_id, msg_id);

CREATE TABLE IF NOT EXISTS room_summaries (
    room_id TEXT PRIMARY KEY,
    summary_text TEXT NOT NULL DEFAULT '',
    last_summarized_seq INTEGER NOT NULL DEFAULT 0,
    updated_at_ms INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS memories (
    id TEXT PRIMARY KEY,
    room_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('episodic', 'semantic', 'habit')),
    content TEXT NOT NULL,
    t_event_ms INTEGER NOT NULL,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    last_recalled_ms INTEGER NOT NULL DEFAULT 0,
    recall_count INTEGER NOT NULL DEFAULT 0,
    importance INTEGER NOT NULL DEFAULT 5,
    superseded_by TEXT NOT NULL DEFAULT '',
    archived INTEGER NOT NULL DEFAULT 0,
    embedding BLOB,
    embedding_dim INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_memories_room_active ON memories(room_id, archived, superseded_by);
CREATE INDEX IF NOT EXISTS idx_memories_t_event ON memories(t_event_ms);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    memory_id UNINDEXED,
    room_id UNINDEXED,
    content_tokens,
    entities_tokens,
    tokenize='unicode61'
);

CREATE TABLE IF NOT EXISTS memory_entities (
    memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    canonical_entity TEXT NOT NULL,
    PRIMARY KEY (memory_id, canonical_entity)
);
CREATE INDEX IF NOT EXISTS idx_memory_entities_canonical ON memory_entities(canonical_entity);

CREATE TABLE IF NOT EXISTS entity_aliases (
    room_id TEXT NOT NULL,
    alias TEXT NOT NULL,
    canonical_entity TEXT NOT NULL,
    sender_id TEXT NOT NULL DEFAULT '',
    updated_at_ms INTEGER NOT NULL,
    PRIMARY KEY (room_id, alias)
);
CREATE INDEX IF NOT EXISTS idx_entity_aliases_canonical ON entity_aliases(room_id, canonical_entity);
CREATE INDEX IF NOT EXISTS idx_entity_aliases_sender ON entity_aliases(room_id, sender_id);

CREATE TABLE IF NOT EXISTS persona_core (
    room_id TEXT PRIMARY KEY DEFAULT 'default',
    beliefs TEXT NOT NULL DEFAULT '',
    temperament_json TEXT NOT NULL DEFAULT '{}',
    legacy_prompt TEXT NOT NULL DEFAULT '',
    updated_at_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS persona_traits (
    id TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    position INTEGER NOT NULL,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_persona_traits_pos ON persona_traits(position);

CREATE TABLE IF NOT EXISTS persona_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    reason TEXT NOT NULL DEFAULT '',
    core_json TEXT NOT NULL,
    traits_json TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pad_states (
    room_id TEXT PRIMARY KEY,
    p REAL NOT NULL,
    a REAL NOT NULL,
    d REAL NOT NULL,
    prototype_id TEXT NOT NULL DEFAULT '',
    updated_at_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pad_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    room_id TEXT NOT NULL,
    run_id TEXT NOT NULL DEFAULT '',
    p REAL NOT NULL,
    a REAL NOT NULL,
    d REAL NOT NULL,
    delta_p REAL NOT NULL DEFAULT 0,
    delta_a REAL NOT NULL DEFAULT 0,
    delta_d REAL NOT NULL DEFAULT 0,
    prototype_id TEXT NOT NULL DEFAULT '',
    created_at_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pad_history_room_time ON pad_history(room_id, created_at_ms DESC);

CREATE TABLE IF NOT EXISTS memes (
    hash TEXT PRIMARY KEY,
    rel_path TEXT NOT NULL,
    thumb_rel_path TEXT NOT NULL DEFAULT '',
    phash TEXT NOT NULL DEFAULT '',
    overlay_text TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    emotions TEXT NOT NULL DEFAULT '',
    scenes TEXT NOT NULL DEFAULT '',
    search_blob TEXT NOT NULL DEFAULT '',
    embedding BLOB,
    embedding_dim INTEGER NOT NULL DEFAULT 0,
    use_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memes_status_use ON memes(status, use_count DESC);
CREATE INDEX IF NOT EXISTS idx_memes_phash ON memes(phash);

CREATE TABLE IF NOT EXISTS meme_seen (
    hash TEXT PRIMARY KEY,
    phash TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'seen',
    label TEXT NOT NULL DEFAULT '',
    created_at_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_meme_seen_phash ON meme_seen(phash);

CREATE TABLE IF NOT EXISTS run_traces (
    run_id TEXT PRIMARY KEY,
    room_id TEXT NOT NULL,
    trigger_source TEXT NOT NULL DEFAULT '',
    started_at_ms INTEGER NOT NULL,
    finished_at_ms INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'running',
    gate_json TEXT NOT NULL DEFAULT '{}',
    pad_json TEXT NOT NULL DEFAULT '{}',
    memories_json TEXT NOT NULL DEFAULT '[]',
    steps_json TEXT NOT NULL DEFAULT '[]',
    actions_json TEXT NOT NULL DEFAULT '[]',
    error_text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_run_traces_room_started ON run_traces(room_id, started_at_ms DESC);
CREATE INDEX IF NOT EXISTS idx_run_traces_started ON run_traces(started_at_ms DESC);

CREATE TABLE IF NOT EXISTS dynamic_config (
    key TEXT PRIMARY KEY,
    value_json TEXT NOT NULL,
    updated_at_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS secrets (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at_ms INTEGER NOT NULL
);
