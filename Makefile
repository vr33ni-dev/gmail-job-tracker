DB=docker exec -i gmail-job-tracker-db psql -U postgres -d gmail_job_tracker

.PHONY: db-truncate db-reset db-acto db-list db-corrections db-normalize

db-truncate:
	$(DB) -c "TRUNCATE applications, status_events, processed_emails RESTART IDENTITY CASCADE;"

db-reset:
	$(DB) -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
	@echo "Schema reset. Run 'make db-seed' to apply personal config."

db-acto:
	$(DB) -c "SELECT id, company, role, status, applied_at FROM applications WHERE company ILIKE '%acto%' ORDER BY applied_at;"

db-list:
	$(DB) -c "SELECT id, company, role, status FROM applications ORDER BY company, id;"

db-corrections:
	$(DB) -c "SELECT wrong_status, correct_status, email_subject FROM corrections;"

db-normalize:
	$(DB) -c "UPDATE applications SET role = TRIM(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(role,' (m/f/d)',''),' (m/w/d)',''),' (f/m/d)',''),' (m/f/x)',''),' (f/m/x)','')) WHERE role LIKE '%(m/%' OR role LIKE '%(f/%';"

db-fix-roles:
	$(DB) -c "UPDATE applications a SET role = ( SELECT role FROM applications b WHERE LOWER(b.company) = LOWER(a.company) AND b.role != '' ORDER BY b.applied_at DESC LIMIT 1 ) WHERE a.role = '' AND EXISTS ( SELECT 1 FROM applications b WHERE LOWER(b.company) = LOWER(a.company) AND b.role != '' );"

correct:
	@read -p "App ID: " id; read -p "Status: " status; \
	$(DB) -c "INSERT INTO corrections (email_id, email_subject, email_body, wrong_status, correct_status) SELECT last_email_id, '', LEFT(email_body, 500), status, '$$status' FROM applications WHERE id=$$id;"; \
	$(DB) -c "UPDATE applications SET status='$$status', updated_at=NOW() WHERE id=$$id;"

db-company:
	@read -p "Company: " company; \
	$(DB) -c "SELECT id, company, role, status, applied_at FROM applications WHERE company ILIKE '%$$company%' ORDER BY applied_at;"

# make db-company
# enter: Bluefish
# see all IDs for Bluefish
# make withdraw id=123
withdraw:
	$(DB) -c "INSERT INTO applications (company, role, platform, applied_at, status, last_email_id, email_body, notes, url, language) SELECT company, role, platform, NOW(), 'withdrawn', '', '', '', '', language FROM applications WHERE id=$(id) LIMIT 1;"

db-add-alias:
	@read -p "Alias: " alias; read -p "Canonical: " canonical; \
	$(DB) -c "INSERT INTO company_aliases VALUES ('$$alias', '$$canonical') ON CONFLICT DO NOTHING;"

db-add-applied:
	@read -p "Company: " company; \
	read -p "Role: " role; \
	read -p "Date (YYYY-MM-DD): " date; \
	read -p "Platform: " platform; \
	$(DB) -c "INSERT INTO applications (company, role, platform, applied_at, status, last_email_id, email_body, notes, url, language) VALUES ('$$company', '$$role', '$$platform', '$$date', 'applied', '', '', '', '', 'en');"

db-settings:
	$(DB) -c "SELECT key, value FROM settings;"

db-set-user:
	@read -p "Email: " email; read -p "Name: " name; \
	$(DB) -c "UPDATE settings SET value='$$email' WHERE key='user_email'; UPDATE settings SET value='$$name' WHERE key='user_name';"

db-seed:
	$(DB) < seeds.sql

db-fresh:
	@echo "1. make db-reset"
	@echo "2. go run cmd/server/main.go (wait for migration, then Ctrl+C)"
	@echo "3. make db-seed"