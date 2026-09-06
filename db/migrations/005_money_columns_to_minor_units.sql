-- Money columns store minor units and must match the int64 fields on the models
ALTER TABLE payments ALTER COLUMN amount TYPE BIGINT USING amount::BIGINT;
ALTER TABLE refunds ALTER COLUMN amount TYPE BIGINT USING amount::BIGINT;
