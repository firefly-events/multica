-- Reports & Insights: persisted visual insights, composed dashboards, report
-- definitions, and immutable execution snapshots.

CREATE TABLE insight (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    owner_id UUID REFERENCES member(id) ON DELETE SET NULL,
    name TEXT NOT NULL,
    chart_type TEXT NOT NULL
        CHECK (chart_type IN ('line', 'area', 'bar', 'stacked_bar', 'pie', 'table', 'number')),
    metric_spec JSONB NOT NULL DEFAULT '{}'::jsonb,
    dimension_spec JSONB NOT NULL DEFAULT '{}'::jsonb,
    filter_spec JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT insight_metric_spec_is_object CHECK (jsonb_typeof(metric_spec) = 'object'),
    CONSTRAINT insight_dimension_spec_is_object CHECK (jsonb_typeof(dimension_spec) = 'object'),
    CONSTRAINT insight_filter_spec_is_object CHECK (jsonb_typeof(filter_spec) = 'object'),
    CONSTRAINT insight_name_not_blank CHECK (length(btrim(name)) > 0),
    -- Composite key target for the composite FK on dashboard_tile
    -- (insight_id, workspace_id) — guarantees a tile can only reference
    -- an insight in its own workspace.
    UNIQUE (id, workspace_id)
);

CREATE UNIQUE INDEX idx_insight_workspace_name_unique ON insight(workspace_id, lower(name));
CREATE INDEX idx_insight_workspace ON insight(workspace_id, created_at DESC);
CREATE INDEX idx_insight_owner ON insight(owner_id) WHERE owner_id IS NOT NULL;

CREATE TABLE dashboard (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    owner_id UUID REFERENCES member(id) ON DELETE SET NULL,
    name TEXT NOT NULL,
    description TEXT,
    layout JSONB NOT NULL DEFAULT '{}'::jsonb,
    filter_override JSONB NOT NULL DEFAULT '{}'::jsonb,
    date_override JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dashboard_layout_is_object CHECK (jsonb_typeof(layout) = 'object'),
    CONSTRAINT dashboard_filter_override_is_object CHECK (jsonb_typeof(filter_override) = 'object'),
    CONSTRAINT dashboard_date_override_is_object CHECK (jsonb_typeof(date_override) = 'object'),
    CONSTRAINT dashboard_name_not_blank CHECK (length(btrim(name)) > 0),
    -- Composite key target for the composite FK on dashboard_tile
    -- (dashboard_id, workspace_id).
    UNIQUE (id, workspace_id)
);

CREATE UNIQUE INDEX idx_dashboard_workspace_name_unique ON dashboard(workspace_id, lower(name));
CREATE INDEX idx_dashboard_workspace ON dashboard(workspace_id, created_at DESC);
CREATE INDEX idx_dashboard_owner ON dashboard(owner_id) WHERE owner_id IS NOT NULL;

-- Dashboard tiles are the reference-not-copy edge: each tile points to an
-- insight and keeps only presentation layout plus optional overrides.
-- workspace_id + the two composite FKs guarantee a tile, its dashboard,
-- and its insight all live in the same workspace.
CREATE TABLE dashboard_tile (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    dashboard_id UUID NOT NULL,
    insight_id UUID NOT NULL,
    position INT NOT NULL DEFAULT 0,
    layout JSONB NOT NULL DEFAULT '{}'::jsonb,
    filter_override JSONB NOT NULL DEFAULT '{}'::jsonb,
    date_override JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dashboard_tile_layout_is_object CHECK (jsonb_typeof(layout) = 'object'),
    CONSTRAINT dashboard_tile_filter_override_is_object CHECK (jsonb_typeof(filter_override) = 'object'),
    CONSTRAINT dashboard_tile_date_override_is_object CHECK (jsonb_typeof(date_override) = 'object'),
    CONSTRAINT dashboard_tile_dashboard_fk
        FOREIGN KEY (dashboard_id, workspace_id)
        REFERENCES dashboard(id, workspace_id)
        ON DELETE CASCADE,
    -- Deliberately NO ACTION (not CASCADE): deleting an insight that is
    -- still on a dashboard must fail — reference-not-copy means the tile
    -- cannot silently lose its insight. DEFERRABLE so a workspace-level
    -- cascade (which removes insights, dashboards, and tiles in one
    -- statement, in FK-creation order) checks at commit instead of
    -- mid-cascade, where tiles still transiently reference deleted
    -- insights. RESTRICT is not used because RESTRICT checks immediately
    -- even when deferred, which re-breaks the workspace cascade.
    CONSTRAINT dashboard_tile_insight_fk
        FOREIGN KEY (insight_id, workspace_id)
        REFERENCES insight(id, workspace_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX idx_dashboard_tile_dashboard ON dashboard_tile(dashboard_id, position, created_at);
CREATE INDEX idx_dashboard_tile_insight ON dashboard_tile(insight_id);

CREATE TABLE report (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    owner_id UUID REFERENCES member(id) ON DELETE SET NULL,
    slug TEXT NOT NULL,
    type TEXT NOT NULL,
    title TEXT NOT NULL,
    template_ref JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT report_template_ref_is_object CHECK (jsonb_typeof(template_ref) = 'object'),
    CONSTRAINT report_slug_format CHECK (slug ~ '^([a-z0-9]|[a-z0-9][a-z0-9-]*[a-z0-9])$'),
    CONSTRAINT report_type_not_blank CHECK (length(btrim(type)) > 0),
    CONSTRAINT report_title_not_blank CHECK (length(btrim(title)) > 0)
);

CREATE UNIQUE INDEX idx_report_workspace_slug_unique ON report(workspace_id, slug);
CREATE INDEX idx_report_workspace_type ON report(workspace_id, type, created_at DESC);
CREATE INDEX idx_report_owner ON report(owner_id) WHERE owner_id IS NOT NULL;

CREATE TABLE report_execution (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    report_id UUID NOT NULL REFERENCES report(id) ON DELETE CASCADE,
    owner_id UUID REFERENCES member(id) ON DELETE SET NULL,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    results_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    narrative_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT report_execution_results_snapshot_is_object CHECK (jsonb_typeof(results_snapshot) = 'object'),
    CONSTRAINT report_execution_narrative_snapshot_is_object CHECK (jsonb_typeof(narrative_snapshot) = 'object')
);

CREATE INDEX idx_report_execution_report_generated ON report_execution(report_id, generated_at DESC);
CREATE INDEX idx_report_execution_workspace_generated ON report_execution(workspace_id, generated_at DESC);
CREATE INDEX idx_report_execution_owner ON report_execution(owner_id) WHERE owner_id IS NOT NULL;

CREATE FUNCTION prevent_report_execution_update() RETURNS trigger AS $$
BEGIN
    -- Tolerate exactly one transition: owner_id -> NULL with every other
    -- column unchanged. That is what ON DELETE SET NULL on owner_id emits
    -- when a member is deleted; without this carve-out, deleting a member
    -- who owns any execution aborts on this trigger.
    IF NEW.owner_id IS NULL
       AND (to_jsonb(NEW) - 'owner_id') = (to_jsonb(OLD) - 'owner_id') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'report_execution rows are immutable';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER report_execution_immutable
    BEFORE UPDATE ON report_execution
    FOR EACH ROW
    EXECUTE FUNCTION prevent_report_execution_update();
