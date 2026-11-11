CREATE TABLE blocks (
    network         TEXT             NOT NULL,
    number          BIGINT           NOT NULL,
    base_fee        NUMERIC(78,0)    NOT NULL,
    gas_used_ratio  DOUBLE PRECISION NOT NULL,
    required_tip    NUMERIC(78,0)    NOT NULL,
    median_tip      NUMERIC(78,0)    NOT NULL,
    observed_at     TIMESTAMPTZ      NOT NULL,
    PRIMARY KEY (network, number)
);
CREATE INDEX blocks_observed_at ON blocks (observed_at);

CREATE TABLE snapshots (
    id                BIGSERIAL PRIMARY KEY,
    network           TEXT          NOT NULL,
    at_block          BIGINT        NOT NULL,
    observed_at       TIMESTAMPTZ   NOT NULL,
    next_base_fee     NUMERIC(78,0) NOT NULL,
    node_tip          NUMERIC(78,0) NOT NULL,
    native_usd        NUMERIC(38,18),
    price_updated_at  TIMESTAMPTZ,
    l1_fees           JSONB         NOT NULL DEFAULT '{}'
);

CREATE TABLE predictions (
    id                    BIGSERIAL PRIMARY KEY,
    snapshot_id           BIGINT           NOT NULL REFERENCES snapshots(id),
    network               TEXT             NOT NULL,
    predictor             TEXT             NOT NULL,
    horizon_seconds       INTEGER          NOT NULL,
    horizon_blocks        INTEGER          NOT NULL,
    at_block              BIGINT           NOT NULL,
    max_fee_per_gas       NUMERIC(78,0)    NOT NULL,
    tip_per_gas           NUMERIC(78,0)    NOT NULL,
    expected_base_fee     NUMERIC(78,0)    NOT NULL,
    status                TEXT             NOT NULL DEFAULT 'pending',
    resolved_at           TIMESTAMPTZ,
    included              BOOLEAN,
    inclusion_block       BIGINT,
    paid_per_gas          NUMERIC(78,0),
    required_per_gas      NUMERIC(78,0),
    overpay_per_gas       NUMERIC(78,0),
    estimate_error_ratio  DOUBLE PRECISION
);
CREATE INDEX predictions_pending ON predictions (network, at_block) WHERE status = 'pending';
