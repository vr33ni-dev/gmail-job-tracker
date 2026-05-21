-- Demo application: Acme Corp — used to demonstrate correction + rule suggestion flow.
-- The interview stage points at a Zoom calendar notification email (easy to correct to
-- "conversation only"), which triggers the rule suggestion modal in the UI.

INSERT INTO applications (company, role, platform, language, applied_at)
SELECT 'Acme Corp', 'Backend Engineer', 'linkedin', 'en', NOW() - INTERVAL '5 days'
WHERE NOT EXISTS (
  SELECT 1 FROM applications
  WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer'
);

INSERT INTO thread_emails (email_id, thread_id, application_id, subject, body, from_addr, email_date)
SELECT
  'fake-email-001',
  'fake-thread-001',
  id,
  'Zoom: Interview with Acme Corp — Backend Engineer',
  'You have a scheduled Zoom meeting. Join Zoom Meeting https://zoom.us/j/99999999 Meeting ID: 999 9999 9999 Passcode: acme This is an automated calendar notification.',
  'no-reply@zoom.us',
  NOW() - INTERVAL '2 days'
FROM applications
WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer'
  AND NOT EXISTS (SELECT 1 FROM thread_emails WHERE email_id='fake-email-001');

INSERT INTO application_stages (application_id, status, last_email_id, applied_at)
SELECT id, 'applied', '', NOW() - INTERVAL '5 days'
FROM applications
WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer'
  AND NOT EXISTS (
    SELECT 1 FROM application_stages
    WHERE application_id = (
      SELECT id FROM applications WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer' LIMIT 1
    ) AND status='applied'
  );

INSERT INTO application_stages (application_id, status, last_email_id, applied_at)
SELECT id, 'interview', 'fake-email-001', NOW() - INTERVAL '2 days'
FROM applications
WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer'
  AND NOT EXISTS (SELECT 1 FROM application_stages WHERE last_email_id='fake-email-001');

-- Second fake email: a real interview invitation
INSERT INTO thread_emails (email_id, thread_id, application_id, subject, body, from_addr, email_date)
SELECT
  'fake-email-003',
  'fake-thread-001',
  id,
  'Interview invitation — Backend Engineer at Acme Corp',
  'Hi Verena, I reviewed your profile and would love to schedule a first interview. Please pick a slot that works for you: https://calendly.com/acme-sarah/interview Looking forward to speaking with you! Sarah Miller, Engineering Lead at Acme Corp',
  'sarah.miller@acmecorp.com',
  NOW() - INTERVAL '3 days'
FROM applications
WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer'
  AND NOT EXISTS (SELECT 1 FROM thread_emails WHERE email_id='fake-email-003');

INSERT INTO application_stages (application_id, status, last_email_id, applied_at)
SELECT id, 'interview', 'fake-email-003', NOW() - INTERVAL '3 days'
FROM applications
WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer'
  AND NOT EXISTS (SELECT 1 FROM application_stages WHERE last_email_id='fake-email-003');

-- Third fake email: a Gmail reaction notification — clearly not an interview stage
INSERT INTO thread_emails (email_id, thread_id, application_id, subject, body, from_addr, email_date)
SELECT
  'fake-email-002',
  'fake-thread-001',
  id,
  'Sarah reacted to your message',
  'Sarah Miller reacted 👍 to your message: "Looking forward to speaking with you on Thursday!" View conversation https://mail.google.com/mail/u/0/#inbox/fake-thread-001',
  'noreply@google.com',
  NOW() - INTERVAL '1 day'
FROM applications
WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer'
  AND NOT EXISTS (SELECT 1 FROM thread_emails WHERE email_id='fake-email-002');

INSERT INTO application_stages (application_id, status, last_email_id, applied_at)
SELECT id, 'interview', 'fake-email-002', NOW() - INTERVAL '1 day'
FROM applications
WHERE LOWER(company)='acme corp' AND LOWER(role)='backend engineer'
  AND NOT EXISTS (SELECT 1 FROM application_stages WHERE last_email_id='fake-email-002');

-- Link thread_emails.stage_id → application_stages.id so GetJourney can group them
UPDATE thread_emails te
SET stage_id = s.id
FROM application_stages s
WHERE te.email_id = s.last_email_id
  AND te.email_id IN ('fake-email-001', 'fake-email-002', 'fake-email-003');

INSERT INTO company_aliases VALUES ('pelo.tech', 'Pelotech') ON CONFLICT DO NOTHING;
INSERT INTO company_aliases VALUES ('pelo', 'Pelotech') ON CONFLICT DO NOTHING;
INSERT INTO company_aliases VALUES ('Pelotech', 'Pelotech') ON CONFLICT DO NOTHING;
INSERT INTO company_aliases VALUES ('PeloTech', 'Pelotech') ON CONFLICT DO NOTHING;

