DO $$ BEGIN RAISE NOTICE '[Migration 000092] Creating tenant_plugins and snapshots'; END $$;

CREATE TABLE IF NOT EXISTS tenant_plugins (
    id                  VARCHAR(36)   PRIMARY KEY,
    tenant_id           BIGINT        NULL,
    plugin_id           VARCHAR(255)  NOT NULL,
    kind                VARCHAR(64)   NOT NULL,
    channel             VARCHAR(32)   NOT NULL,
    transport           VARCHAR(32)   NOT NULL,
    endpoint            VARCHAR(1024) NULL,
    policy_class        VARCHAR(32)   NOT NULL,
    source_url          VARCHAR(1024) NULL,
    source_ref          VARCHAR(255)  NULL,
    source_sha          VARCHAR(64)   NULL,
    image_ref           VARCHAR(1024) NULL,
    container_name      VARCHAR(255)  NULL,
    status              VARCHAR(32)   NOT NULL DEFAULT 'installing',
    installing_since    TIMESTAMPTZ   NULL,
    enabled             BOOLEAN       NOT NULL DEFAULT TRUE,
    error               TEXT          NULL,
    envs                JSONB         NOT NULL DEFAULT '{}'::jsonb,
    permissions         JSONB         NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ   NULL
);

-- 表注释
COMMENT ON TABLE tenant_plugins IS 
    '插件主表，支持租户级隔离和进程级全局插件。容器运行时状态（container_name）与 SSRF 白名单联动。';

COMMENT ON COLUMN tenant_plugins.tenant_id IS '租户ID，NULL 表示进程级全局插件（不归属任何租户）';
COMMENT ON COLUMN tenant_plugins.plugin_id IS '清单 ID（含命名空间），与 tenant_id 构成逻辑唯一键';
COMMENT ON COLUMN tenant_plugins.endpoint IS '端点真相源，容器形态下由装载流程写入，用户不可直接修改';
COMMENT ON COLUMN tenant_plugins.envs IS '环境变量 JSON，键值对形式，受 secrets 白名单约束';
COMMENT ON COLUMN tenant_plugins.permissions IS '原始权限声明 JSON，用于审计插件申请了哪些权限';

ALTER TABLE tenant_plugins 
ADD CONSTRAINT chk_tenant_plugins_kind 
CHECK (kind IN ('docparser', 'websearch', 'datasource'));

ALTER TABLE tenant_plugins 
ADD CONSTRAINT chk_tenant_plugins_channel 
CHECK (channel IN ('builtin', 'bundle', 'endpoint'));

ALTER TABLE tenant_plugins 
ADD CONSTRAINT chk_tenant_plugins_transport 
CHECK (transport IN ('remote-grpc', 'remote-http', 'subprocess-grpc'));

ALTER TABLE tenant_plugins 
ADD CONSTRAINT chk_tenant_plugins_policy 
CHECK (policy_class IN ('offline', 'scoped', 'open'));

ALTER TABLE tenant_plugins 
ADD CONSTRAINT chk_tenant_plugins_status 
CHECK (status IN ('installing', 'ready', 'failed', 'removing'));


CREATE UNIQUE INDEX IF NOT EXISTS uq_tenant_plugins_tenant_plugin
    ON tenant_plugins (tenant_id, plugin_id)
    WHERE tenant_id IS NOT NULL AND deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_tenant_plugins_global_plugin
    ON tenant_plugins (plugin_id)
    WHERE tenant_id IS NULL AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tenant_plugins_tenant
    ON tenant_plugins (tenant_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tenant_plugins_status_enabled
    ON tenant_plugins (status, enabled) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tenant_plugins_installing_since
    ON tenant_plugins (installing_since) WHERE status IN ('installing', 'removing') AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_tenant_plugins_container
    ON tenant_plugins (container_name) WHERE container_name IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS tenant_plugin_snapshots (
    id                  VARCHAR(36)   PRIMARY KEY,
    tenant_id           BIGINT        NULL,     
    plugin_id           VARCHAR(255)  NOT NULL,
    snapshot            JSONB         NOT NULL DEFAULT '{}'::jsonb,
    source_sha          VARCHAR(64)   NULL,
    created_at          TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ   NULL     
);

COMMENT ON TABLE tenant_plugin_snapshots IS 
    '插件快照表，结构与 TenantSkillSnapshotEntity 同形，用于 ReconcileSnapshots 对账逻辑。';

CREATE INDEX IF NOT EXISTS idx_plugin_snapshots_tenant_plugin
    ON tenant_plugin_snapshots (tenant_id, plugin_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_plugin_snapshots_created
    ON tenant_plugin_snapshots (created_at) WHERE deleted_at IS NULL;

DO $$ BEGIN RAISE NOTICE '[Migration 000092] tenant_plugins and snapshots created successfully'; END $$;