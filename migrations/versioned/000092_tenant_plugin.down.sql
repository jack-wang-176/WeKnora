DO $$ BEGIN RAISE NOTICE '[Migration 000092 Down] Rolling back tenant_plugins tables'; END $$;

DROP TABLE IF EXISTS tenant_plugin_snapshots CASCADE;
DROP TABLE IF EXISTS tenant_plugins CASCADE;

DO $$ BEGIN RAISE NOTICE '[Migration 000092 Down] Rollback completed'; END $$;