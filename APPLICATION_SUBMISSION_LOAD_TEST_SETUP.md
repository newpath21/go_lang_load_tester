# Application Submission Load Test Setup

SQL seed commands and API reference for load testing
`PATCH /api/v1/application/{id}/submit`.

**Test user password:** `LoadTest@2026`
**bcrypt hash (pre-computed):** `$2b$12$OPUkO3y3unIB9AhLg09L5OqUcjqAnl8AOX.LrF0yBkCtW31fFNyBy`

---

## What the Submission Flow Executes

Each call to `/submit` runs through the `ApplicationStateMachine` (DRAFTED → SUBMITTED):

| Step | Operation | Notes |
|------|-----------|-------|
| 1 | `_validate_submission_deadline` | Checks `CallApplication.end_date` — **cannot be bypassed** |
| 2 | `_validate_required_files_uploaded` | Skipped if user has `submitted_applications` permission |
| 3 | `_validate_signed_document_uploaded` | Skipped if user has `submitted_applications` permission |
| 4 | `_validate_required_contacts` | Skipped if user lacks `manage_contact_information` permission |
| 5 | `_validate_eligibility_requirements` | Skipped if user has `submitted_applications` permission |
| 6 | `_calculate_application_score` | Scores self-declaration answers |
| 7 | `_run_place_distribution_algorithm` | **The expensive part** — re-ranks all submitted apps in the call |

To bypass validators and focus testing on steps 6–7, test users are given both the
**Applications Submitter** (`submit_applications`) and **Applications Specialist**
(`submitted_applications`) roles. The `end_date = NULL` on the CallApplication
removes the deadline check.

---

## Step 0: Discover Role IDs (run first)

```sql
-- Find roles that carry the two required permissions
SELECT r.id, r.name, p.name AS permission
FROM public."Role" r
JOIN public."rolepermissionlink" rpl ON rpl.role_id = r.id AND rpl.is_granted = true
JOIN public."permission" p ON p.id = rpl.permission_id
WHERE p.name IN ('submit_applications', 'submitted_applications')
ORDER BY r.name, p.name;
```

Expected results:
- `Applications Submitter` → `submit_applications`
- `Applications Specialist` → `submitted_applications`

---

## Step 1: Create 20 Test Banks (class A, not NRB)

```sql
INSERT INTO public."Bank" (
    name, bank_class, working_area,
    is_active, is_central, is_operating,
    head_office, created_at, updated_at
)
SELECT
    'LoadTest Bank ' || lpad(g::text, 3, '0'),
    'A',
    'Kathmandu',
    true,
    false,
    false,
    'Kathmandu',
    now(),
    now()
FROM generate_series(1, 20) AS g;
```

Verify:

```sql
SELECT id, name, bank_class
FROM public."Bank"
WHERE name LIKE 'LoadTest Bank%'
ORDER BY id;
```

---

## Step 2: Create 20 Test Users (one per bank)

Each user is linked to one of the 20 banks created above (matched by insertion order).

```sql
INSERT INTO public."User" (
    first_name, last_name, email, phone,
    is_superuser, bank_id,
    hashed_password, status,
    created_at, updated_at
)
SELECT
    'LoadTest'                                                          AS first_name,
    'Submitter' || lpad(g::text, 3, '0')                               AS last_name,
    'loadtest_submitter' || lpad(g::text, 3, '0') || '@test.com'       AS email,
    '+77000000' || lpad(g::text, 4, '0')                               AS phone,
    false                                                               AS is_superuser,
    b.id                                                                AS bank_id,
    '$2b$12$OPUkO3y3unIB9AhLg09L5OqUcjqAnl8AOX.LrF0yBkCtW31fFNyBy'   AS hashed_password,
    'Active'                                                            AS status,
    now()                                                               AS created_at,
    now()                                                               AS updated_at
FROM generate_series(1, 20) AS g
JOIN (
    SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS rn
    FROM public."Bank"
    WHERE name LIKE 'LoadTest Bank%'
    ORDER BY id
) b ON b.rn = g;
```

Verify:

```sql
SELECT u.id, u.email, u.status, b.name AS bank
FROM public."User" u
JOIN public."Bank" b ON b.id = u.bank_id
WHERE u.email LIKE 'loadtest_submitter%@test.com'
ORDER BY u.id;
```

---

## Step 3: Assign Roles to Test Users

Two role assignments per user:
1. **Applications Submitter** — grants `submit_applications` (required to call the endpoint)
2. **Applications Specialist** — grants `submitted_applications` (bypasses file/eligibility validators)

```sql
-- Applications Submitter role
INSERT INTO public."UserRoleLink" (user_id, role_id, status, created_at, updated_at)
SELECT u.id, r.id, 'Active', now(), now()
FROM public."User" u
CROSS JOIN public."Role" r
WHERE u.email LIKE 'loadtest_submitter%@test.com'
  AND r.name = 'Applications Submitter'
ON CONFLICT (user_id, role_id, status) DO NOTHING;

-- Applications Specialist role (validator bypass)
INSERT INTO public."UserRoleLink" (user_id, role_id, status, created_at, updated_at)
SELECT u.id, r.id, 'Active', now(), now()
FROM public."User" u
CROSS JOIN public."Role" r
WHERE u.email LIKE 'loadtest_submitter%@test.com'
  AND r.name = 'Applications Specialist'
ON CONFLICT (user_id, role_id, status) DO NOTHING;
```

Verify:

```sql
SELECT u.email, r.name AS role, url.status
FROM public."User" u
JOIN public."UserRoleLink" url ON url.user_id = u.id
JOIN public."Role" r ON r.id = url.role_id
WHERE u.email LIKE 'loadtest_submitter%@test.com'
ORDER BY u.email, r.name;
```

---

## Step 4: Create the Load Test CallApplication

`end_date = NULL` disables the deadline check so submissions are always allowed.
`interest_rate = 'LOAD_TEST_MARKER'` is a unique tag used by all subsequent queries
to locate this record automatically — no manual ID substitution needed.

```sql
INSERT INTO public."CallApplication" (
    start_date, end_date,
    min_amnt, max_amnt, currency,
    maturity, grace, status,
    interest_rate, repayment_principal, payment_interest,
    pfi_selection_timeline,
    created_at, updated_at
)
VALUES (
    '2026-01-01',
    NULL,                    -- NULL = no deadline, bypass _validate_submission_deadline
    500000,
    5000000,
    'Nepalese rupee',
    60,
    6,
    'Open',
    'LOAD_TEST_MARKER',      -- unique tag used to reference this record in steps 5–10
    'Semi-annual',
    'Semi-annual',
    '2026-12-31',
    now(),
    now()
);
```

Verify:

```sql
SELECT id, status, start_date, end_date, interest_rate
FROM public."CallApplication"
WHERE interest_rate = 'LOAD_TEST_MARKER'
ORDER BY id DESC
LIMIT 1;
```

---

## Step 5: Create CallAwardPlaces (required for place distribution)

The `_run_place_distribution_algorithm` executor runs on every submission.
It needs at least one `CallAwardPlace` record to assign results.

```sql
WITH lt_call AS (
    SELECT id FROM public."CallApplication"
    WHERE interest_rate = 'LOAD_TEST_MARKER'
    ORDER BY id DESC
    LIMIT 1
)
INSERT INTO public."CallAwardPlace" (
    call_application_id, place, max_amount, description,
    created_at, updated_at
)
SELECT lt_call.id, places.place, places.max_amount, places.description, now(), now()
FROM lt_call
CROSS JOIN (VALUES
    (1, 5000000.0, '1st Place Award'),
    (2, 3000000.0, '2nd Place Award'),
    (3, 1000000.0, '3rd Place Award')
) AS places(place, max_amount, description);
```

---

## Step 6: Create 20 Draft Applications

One application per bank. Each gets a unique `PFI` prefixed `LT-PFI-` for easy cleanup.

```sql
-- 6a: Insert applications
WITH lt_call AS (
    SELECT id FROM public."CallApplication"
    WHERE interest_rate = 'LOAD_TEST_MARKER'
    ORDER BY id DESC
    LIMIT 1
)
INSERT INTO public."Application" (
    application_amnt, "PFI", number, status,
    score, created, last_updated,
    user_action_req, oper_spst_action_req, submitted,
    bank_id, call_application_id,
    created_at, updated_at
)
SELECT
    500000.0 + (g * 100000.0)                       AS application_amnt,
    'LT-PFI-' || lpad(g::text, 3, '0')              AS "PFI",
    9000 + g                                         AS number,
    'Drafted'                                        AS status,
    NULL                                             AS score,
    CURRENT_DATE                                     AS created,
    CURRENT_DATE                                     AS last_updated,
    false                                            AS user_action_req,
    false                                            AS oper_spst_action_req,
    false                                            AS submitted,
    b.id                                             AS bank_id,
    lt_call.id                                       AS call_application_id,
    now()                                            AS created_at,
    now()                                            AS updated_at
FROM generate_series(1, 20) AS g
CROSS JOIN lt_call
JOIN (
    SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS rn
    FROM public."Bank"
    WHERE name LIKE 'LoadTest Bank%'
    ORDER BY id
) b ON b.rn = g;

-- 6b: Create a SelfDeclaration for each application (required by model relationship)
INSERT INTO public."SelfDeclaration" (application_id, created_at, updated_at)
SELECT a.id, now(), now()
FROM public."Application" a
JOIN public."CallApplication" ca ON ca.id = a.call_application_id
WHERE ca.interest_rate = 'LOAD_TEST_MARKER'
  AND a."PFI" LIKE 'LT-PFI-%';
```

Verify full setup:

```sql
SELECT
    u.email,
    a.id        AS application_id,
    a."PFI",
    a.status,
    b.name      AS bank,
    sd.id       AS self_decl_id
FROM public."User" u
JOIN public."Bank"             b  ON b.id = u.bank_id
JOIN public."CallApplication"  ca ON ca.interest_rate = 'LOAD_TEST_MARKER'
JOIN public."Application"      a  ON a.bank_id = b.id
                                 AND a.call_application_id = ca.id
                                 AND a."PFI" LIKE 'LT-PFI-%'
LEFT JOIN public."SelfDeclaration" sd ON sd.application_id = a.id
WHERE u.email LIKE 'loadtest_submitter%@test.com'
ORDER BY u.email;
```

---

## Step 7: Get the User → Application Mapping

Before running load tests, extract the mapping your test script needs:

```sql
SELECT
    u.id            AS user_id,
    u.email,
    a.id            AS application_id,
    a."PFI"
FROM public."User" u
JOIN public."Bank"            b  ON b.id = u.bank_id
JOIN public."CallApplication" ca ON ca.interest_rate = 'LOAD_TEST_MARKER'
JOIN public."Application"     a  ON a.bank_id = b.id
                                AND a.call_application_id = ca.id
                                AND a."PFI" LIKE 'LT-PFI-%'
WHERE u.email LIKE 'loadtest_submitter%@test.com'
ORDER BY u.email;
```

---

## Step 8: API Calls

### 8.1 Login — get a JWT access token

**Endpoint:** `POST /api/v1/login`

**Body:**
```json
{
  "email": "loadtest_submitter001@test.com",
  "password": "LoadTest@2026"
}
```

**Response shape:**
```json
{
  "data": {
    "access_token": "eyJhbGci...",
    "refresh_token": "...",
    "token_type": "bearer"
  }
}
```

curl example:
```bash
curl -s -X POST http://localhost/api/v1/login \
  -H "Content-Type: application/json" \
  -d '{"email": "loadtest_submitter001@test.com", "password": "LoadTest@2026"}'
```

### 8.2 Submit an Application

**Endpoint:** `PATCH /api/v1/application/{application_id}/submit`

**No request body.**
**Required header:** `Authorization: Bearer <access_token>`

Each user must submit **their own** application (the one belonging to their bank).

curl example:
```bash
# Replace <APP_ID> with the application_id from Step 7
# Replace <TOKEN> with the access_token from Step 8.1

curl -s -X PATCH "http://localhost/api/v1/application/<APP_ID>/submit" \
  -H "Authorization: Bearer <TOKEN>" \
  -H "Content-Type: application/json"
```

Sequential submission loop (all 20 users):
```bash
# Requires jq — install with: brew install jq
for i in $(seq -w 1 20); do
  TOKEN=$(curl -s -X POST http://localhost/api/v1/login \
    -H "Content-Type: application/json" \
    -d "{\"email\": \"loadtest_submitter0${i}@test.com\", \"password\": \"LoadTest@2026\"}" \
    | jq -r '.data.access_token')

  APP_ID=$(psql -t -c "
    SELECT a.id FROM public.\"Application\" a
    JOIN public.\"Bank\" b ON b.id = a.bank_id
    JOIN public.\"User\" u ON u.bank_id = b.id
    WHERE u.email = 'loadtest_submitter0${i}@test.com'
      AND a.\"PFI\" LIKE 'LT-PFI-%'
    LIMIT 1;")

  curl -s -X PATCH "http://localhost/api/v1/application/${APP_ID}/submit" \
    -H "Authorization: Bearer ${TOKEN}" \
    -o /dev/null -w "submitter${i} app${APP_ID}: %{http_code} %{time_total}s\n"
done
```

---

## Step 9: Reset Applications Between Load Test Runs

Reset all 20 applications back to DRAFTED without recreating data.

```sql
UPDATE public."Application"
SET
    status               = 'Drafted',
    submitted            = false,
    submission_date      = NULL,
    score                = NULL,
    place                = NULL,
    awarded_amount       = NULL,
    result_status        = NULL,
    excluded_from_places = NULL,
    can_exclude          = false,
    user_action_req      = false,
    oper_spst_action_req = false,
    updated_at           = now()
WHERE call_application_id = (
    SELECT id FROM public."CallApplication"
    WHERE interest_rate = 'LOAD_TEST_MARKER'
    ORDER BY id DESC LIMIT 1
)
  AND "PFI" LIKE 'LT-PFI-%';
```

Also reset the `CallAwardPlace.awarded_bank_id` after each run:

```sql
UPDATE public."CallAwardPlace"
SET awarded_bank_id = NULL, updated_at = now()
WHERE call_application_id = (
    SELECT id FROM public."CallApplication"
    WHERE interest_rate = 'LOAD_TEST_MARKER'
    ORDER BY id DESC LIMIT 1
);
```

---

## Step 10: Full Cleanup

Run after load testing is complete.

```sql
-- 1. Self-declarations
DELETE FROM public."SelfDeclaration"
WHERE application_id IN (
    SELECT a.id FROM public."Application" a
    JOIN public."CallApplication" ca ON ca.id = a.call_application_id
    WHERE ca.interest_rate = 'LOAD_TEST_MARKER' AND a."PFI" LIKE 'LT-PFI-%'
);

-- 2. Applications
DELETE FROM public."Application"
WHERE call_application_id = (
    SELECT id FROM public."CallApplication"
    WHERE interest_rate = 'LOAD_TEST_MARKER' ORDER BY id DESC LIMIT 1
)
  AND "PFI" LIKE 'LT-PFI-%';

-- 3. Award places
DELETE FROM public."CallAwardPlace"
WHERE call_application_id = (
    SELECT id FROM public."CallApplication"
    WHERE interest_rate = 'LOAD_TEST_MARKER' ORDER BY id DESC LIMIT 1
);

-- 4. CallApplication
DELETE FROM public."CallApplication" WHERE interest_rate = 'LOAD_TEST_MARKER';

-- 5. User role links
DELETE FROM public."UserRoleLink"
WHERE user_id IN (
    SELECT id FROM public."User" WHERE email LIKE 'loadtest_submitter%@test.com'
);

-- 6. Test users
DELETE FROM public."User" WHERE email LIKE 'loadtest_submitter%@test.com';

-- 7. Test banks
DELETE FROM public."Bank" WHERE name LIKE 'LoadTest Bank%';
```

---

## Quick Reference

| Entity | Count | Identifier pattern |
|--------|-------|--------------------|
| Banks | 20 | `name LIKE 'LoadTest Bank%'` |
| Users | 20 | `email LIKE 'loadtest_submitter%@test.com'` |
| Applications | 20 | `"PFI" LIKE 'LT-PFI-%'` |
| Password | — | `LoadTest@2026` |

| API action | Method + Path |
|------------|---------------|
| Login | `POST /api/v1/login` |
| Submit application | `PATCH /api/v1/application/{id}/submit` |
