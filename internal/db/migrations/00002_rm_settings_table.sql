-- +goose Up
DROP TABLE IF EXISTS settings;

-- +goose Down
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);