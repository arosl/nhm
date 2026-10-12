package store

// migrations[i] moves the database from user_version i to i+1. Append a
// migration for a schema change; never edit one that a release shipped.
// A release refuses a database whose user_version is newer than it knows,
// so `nhm admin rollback` can say what it cannot read (plan-v8 section 4).
//
// The tables are those of plan-v8 section 1. The bead that owns a feature
// owns its table's later migrations: bindings and slots (nhm.bindings),
// operations and receipts (nhm.send, nhm.spawn), injections and feedback
// (nhm.memory), wake (nhm.wake), hook_candidates (nhm.hooks), transcripts
// (nhm.lifecycle), quota (nhm.quota).
var migrations = []string{
	// 1: the tables of plan-v8 section 1.
	`
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- A pane bound to an Agent Mail identity. generation counts occupants of
-- the identity; herdr's counters are separate (plan-v8 2.3, 2.6).
CREATE TABLE bindings (
	binding_id          TEXT PRIMARY KEY,
	generation          INTEGER NOT NULL CHECK (generation >= 1),
	status              TEXT NOT NULL CHECK (status IN ('active', 'displaced', 'retired')),
	mail_name           TEXT NOT NULL,
	mail_project_key    TEXT NOT NULL,
	project_id          TEXT NOT NULL,
	workspace_id        TEXT,
	pane_id             TEXT,
	herdr_alias         TEXT,
	kind                TEXT NOT NULL,
	model               TEXT,
	launch_id           TEXT,
	native_session_json TEXT,
	foreground_pid      INTEGER,
	foreground_start    INTEGER,
	identity_provenance TEXT NOT NULL CHECK (identity_provenance IN ('created', 'adopted', 'imported')),
	created_by_op       TEXT,
	compacted_at        TEXT,
	resumed_at          TEXT,
	created_at          TEXT NOT NULL,
	updated_at          TEXT NOT NULL
);
-- One active binding per identity, and one per pane.
CREATE UNIQUE INDEX bindings_active_identity ON bindings (mail_project_key, mail_name) WHERE status = 'active';
CREATE UNIQUE INDEX bindings_active_pane ON bindings (pane_id) WHERE status = 'active' AND pane_id IS NOT NULL;

-- ntm's pane numbers (N.M) per workspace.
CREATE TABLE slots (
	workspace_id TEXT NOT NULL,
	slot         TEXT NOT NULL,
	binding_id   TEXT NOT NULL REFERENCES bindings (binding_id),
	assigned_at  TEXT NOT NULL,
	PRIMARY KEY (workspace_id, slot)
);

-- Every mutating verb. The insert of a row is the claim of its id, made
-- before any external effect (plan-v8 section 1, "Idempotency").
CREATE TABLE operations (
	operation_id TEXT PRIMARY KEY,
	verb         TEXT NOT NULL,
	fingerprint  TEXT NOT NULL,
	scope_json   TEXT NOT NULL,
	request_json TEXT NOT NULL,
	status       TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	completed_at TEXT
);

-- One row per target of an operation: the frozen target, the baseline
-- read before the submission, and the result.
CREATE TABLE receipts (
	operation_id   TEXT NOT NULL REFERENCES operations (operation_id),
	target_index   INTEGER NOT NULL,
	binding_id     TEXT,
	generation     INTEGER,
	status         TEXT NOT NULL,
	payload_sha256 TEXT,
	payload_bytes  INTEGER,
	baseline_json  TEXT,
	result_json    TEXT,
	error_code     TEXT,
	updated_at     TEXT NOT NULL,
	PRIMARY KEY (operation_id, target_index)
);

-- CM and CASS context that a send injected.
CREATE TABLE injections (
	injection_id INTEGER PRIMARY KEY,
	operation_id TEXT NOT NULL REFERENCES operations (operation_id),
	binding_id   TEXT,
	source       TEXT NOT NULL,
	query        TEXT,
	items_json   TEXT NOT NULL DEFAULT '[]',
	bytes        INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL
);

-- Outcomes reported back to CM for injected rules.
CREATE TABLE feedback (
	feedback_id  INTEGER PRIMARY KEY,
	injection_id INTEGER REFERENCES injections (injection_id),
	operation_id TEXT,
	binding_id   TEXT,
	outcome      TEXT NOT NULL,
	detail_json  TEXT,
	created_at   TEXT NOT NULL,
	applied_at   TEXT
);

-- Pending Agent Mail message ids per recipient, and their nudges.
CREATE TABLE wake (
	binding_id      TEXT NOT NULL,
	message_id      TEXT NOT NULL,
	thread_id       TEXT,
	sender          TEXT,
	first_seen_at   TEXT NOT NULL,
	wake_after      TEXT,
	attempts        INTEGER NOT NULL DEFAULT 0,
	last_attempt_at TEXT,
	last_outcome    TEXT,
	announced_at    TEXT,
	PRIMARY KEY (binding_id, message_id)
);

-- Evidence that hooks report; nhm verifies it before a binding changes.
CREATE TABLE hook_candidates (
	candidate_id   INTEGER PRIMARY KEY,
	pane_id        TEXT NOT NULL,
	launch_id      TEXT,
	native_session TEXT,
	hook_event     TEXT NOT NULL,
	hook_event_id  TEXT NOT NULL,
	payload_json   TEXT NOT NULL,
	received_at    TEXT NOT NULL,
	processed_at   TEXT,
	result         TEXT,
	UNIQUE (pane_id, hook_event, hook_event_id)
);

-- Transcripts preserved at retire or recycle.
CREATE TABLE transcripts (
	transcript_id  INTEGER PRIMARY KEY,
	binding_id     TEXT NOT NULL,
	generation     INTEGER NOT NULL,
	path           TEXT NOT NULL,
	native_session TEXT,
	reason         TEXT NOT NULL,
	preserved_at   TEXT NOT NULL,
	indexed_at     TEXT
);

-- Quota observations per pool and window.
CREATE TABLE quota (
	pool_id     TEXT NOT NULL,
	window      TEXT NOT NULL,
	used_pct    REAL,
	limited     INTEGER NOT NULL DEFAULT 0,
	resets_at   TEXT,
	source      TEXT NOT NULL,
	observed_at TEXT NOT NULL,
	PRIMARY KEY (pool_id, window)
);
`,
}

// SchemaVersion is the user_version this build writes.
var SchemaVersion = len(migrations)
