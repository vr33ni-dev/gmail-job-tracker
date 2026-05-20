ENV_FILE ?= .env
-include $(ENV_FILE)
export

DB=psql "$(DATABASE_URL)"

.PHONY: db-truncate db-reset db-acto db-list db-corrections db-normalize db-reset-company

db-truncate:
	$(DB) -c "TRUNCATE applications, application_stages, processed_emails, thread_emails RESTART IDENTITY CASCADE;"

db-truncate-all:
	$(DB) -c "TRUNCATE applications, application_stages, processed_emails, thread_emails, corrections RESTART IDENTITY CASCADE;"

# make db-reset ENV_FILE=.env.demo
db-reset:
	$(DB) -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
	@echo "Schema reset. Run 'make db-seed' to apply personal config."

db-corrections:
	$(DB) -c "SELECT wrong_status, correct_status, email_subject FROM corrections;"

# make db-company
# enter: Bluefish
# see all IDs for Bluefish
# make withdraw id=123
withdraw:
	$(DB) -c "INSERT INTO applications (company, role, platform, applied_at, status, last_email_id, email_body, notes, url, language) SELECT company, role, platform, NOW(), 'withdrawn', '', '', '', '', language FROM applications WHERE id=$(id) LIMIT 1;"

db-seed:
	$(DB) < internal/db/seeds.sql

db-corrections-restore:
	$(DB) < internal/db/corrections_backup.sql

db-corrections-backup:
	@echo "-- Seed corrections: run after schema migration on a fresh database." > internal/db/corrections_backup.sql
	@echo "-- These corrections teach the LLM about past misclassifications and company normalizations." >> internal/db/corrections_backup.sql
	@$(DB) -t -A -c "SELECT 'INSERT INTO corrections (email_id, email_subject, email_body, wrong_status, correct_status, command) VALUES' || E'\n' || string_agg('(' || quote_literal(email_id) || ', ' || quote_literal(email_subject) || ', ' || quote_literal(email_body) || ', ' || quote_literal(wrong_status) || ', ' || quote_literal(correct_status) || ', ' || quote_literal(command) || ')', ',' || E'\n') || E'\nON CONFLICT DO NOTHING;' FROM corrections;" >> internal/db/corrections_backup.sql
	@echo "corrections_backup.sql updated"

# make db-reset-company company="Acto"
db-reset-company:
	$(DB) -c "\
	  DELETE FROM processed_emails WHERE email_id IN (\
	    SELECT email_id FROM thread_emails te\
	    JOIN applications a ON a.id = te.application_id\
	    WHERE LOWER(a.company) = LOWER('$(company)')\
	  );\
	  DELETE FROM processed_emails WHERE email_id IN (\
	    SELECT last_email_id FROM application_stages s\
	    JOIN applications a ON a.id = s.application_id\
	    WHERE LOWER(a.company) = LOWER('$(company)') AND s.last_email_id != ''\
	  );\
	  DELETE FROM applications WHERE LOWER(company) = LOWER('$(company)');\
	"
	@echo "$(company) cleared — trigger a company sync from the UI to re-process"

db-fresh:
	@echo "1. make db-reset"
	@echo "2. go run cmd/server/main.go (wait for migration, then Ctrl+C)"
	@echo "3. make db-seed"
	@echo "4. make db-corrections-restore"

lint:
	golangci-lint run ./...

test:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

lint-test: lint test
