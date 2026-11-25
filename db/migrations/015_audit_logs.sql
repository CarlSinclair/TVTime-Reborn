-- +goose Up
CREATE TABLE audit_logs(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL DEFAULT 0,
	action TEXT NOT NULL,
	resource_type TEXT NOT NULL DEFAULT '',
	entity_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'ok',
	details TEXT NOT NULL DEFAULT '{}',
	location TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX idx_audit_created ON audit_logs(created_at);
CREATE INDEX idx_audit_action ON audit_logs(action);
CREATE INDEX idx_audit_user ON audit_logs(user_id);
