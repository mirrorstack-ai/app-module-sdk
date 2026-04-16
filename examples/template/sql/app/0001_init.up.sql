-- App-scoped migrations run per-app-tenant. The platform applies them via the
-- lifecycle install/upgrade routes. Rows are isolated per app by the DB role.
--
-- Replace this example with your module's real schema.

CREATE TABLE IF NOT EXISTS items (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
