CREATE TABLE probes (
    id                  BIGSERIAL PRIMARY KEY,
    network             TEXT          NOT NULL,
    rung                INTEGER       NOT NULL,
    account             TEXT          NOT NULL,
    nonce               BIGINT        NOT NULL,
    tip_wei             NUMERIC(78,0) NOT NULL,
    max_fee_wei         NUMERIC(78,0) NOT NULL,
    tx_hash             TEXT,
    sent_at             TIMESTAMPTZ   NOT NULL,
    head_at_send        BIGINT        NOT NULL,
    included_block      BIGINT,
    included_at_ms      BIGINT,
    outcome             TEXT          NOT NULL,
    error               TEXT,
    replaced_by         TEXT
);
CREATE INDEX probes_network_sent_at ON probes (network, sent_at);
