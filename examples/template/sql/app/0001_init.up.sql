-- App-scoped migrations run per-app-tenant. The platform applies them via the
-- lifecycle install/upgrade routes. Rows are isolated per app by the DB role.
--
-- CONVENTION: every table name MUST start with your module ID + underscore.
-- This prevents collisions when multiple modules share the same app_<id> schema.
-- Example for a module with ID "template":
--   template_items  ✓
--   items           ✗ (will collide with other modules)
--
-- Replace this example with your module's real schema.

CREATE TABLE IF NOT EXISTS template_items (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
