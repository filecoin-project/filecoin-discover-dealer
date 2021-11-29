CREATE SCHEMA IF NOT EXISTS discover;

CREATE OR REPLACE
  FUNCTION discover.valid_cid_v1(TEXT) RETURNS BOOLEAN
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
  SELECT SUBSTRING( $1 FROM 1 FOR 2 ) = 'ba'
$$;


CREATE OR REPLACE
  FUNCTION discover.update_entry_timestamp() RETURNS TRIGGER
    LANGUAGE plpgsql
AS $$
BEGIN
  NEW.entry_last_updated = NOW();
  RETURN NEW;
END;
$$;

CREATE OR REPLACE
  FUNCTION discover.record_deal_event() RETURNS TRIGGER
    LANGUAGE plpgsql
AS $$
BEGIN
  INSERT INTO discover.published_deals_events ( deal_id, status ) VALUES ( NEW.deal_id, NEW.status );
  RETURN NULL;
END;
$$;


CREATE TABLE IF NOT EXISTS discover.providers (
  provider TEXT NOT NULL UNIQUE CONSTRAINT valid_provider_id CHECK ( SUBSTRING( provider FROM 1 FOR 2 ) = 'f0' ),
  active BOOL NOT NULL DEFAULT false,
  entry_created TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
  entry_last_updated TIMESTAMP WITH TIME ZONE NOT NULL,
  details JSONB
);
CREATE TRIGGER trigger_provider_insert
  BEFORE INSERT ON discover.providers
  FOR EACH ROW
  EXECUTE PROCEDURE discover.update_entry_timestamp()
;
CREATE TRIGGER trigger_provider_updated
  BEFORE UPDATE OF details, active ON discover.providers
  FOR EACH ROW
  WHEN (OLD IS DISTINCT FROM NEW)
  EXECUTE PROCEDURE discover.update_entry_timestamp()
;

CREATE TABLE IF NOT EXISTS discover.dataset_groups (
  dataset_group_id SMALLINT NOT NULL UNIQUE,
  label TEXT NOT NULL UNIQUE
);


CREATE TABLE IF NOT EXISTS discover.car_files (
  piece_cid TEXT NOT NULL UNIQUE CONSTRAINT valid_piece_cid CHECK ( discover.valid_cid_v1(piece_cid) ),
  raw_commp BYTEA NOT NULL UNIQUE CONSTRAINT valid_commp CHECK ( LENGTH( raw_commp ) = 32 ),
  root_cid TEXT UNIQUE CONSTRAINT valid_root_cid CHECK ( discover.valid_cid_v1(root_cid) ),
  padded_piece_size INTEGER NOT NULL CONSTRAINT valid_piece_size CHECK ( padded_piece_size > 127 ),
  dataset_group_id SMALLINT NOT NULL REFERENCES discover.dataset_groups( dataset_group_id ),
  meta JSONB
);
CREATE INDEX IF NOT EXISTS car_files_dataset_group_idx ON discover.car_files ( dataset_group_id );
CREATE INDEX IF NOT EXISTS car_files_pending_key ON discover.car_files ( (meta->>'dynamo_root'), (meta->>'payload_size') ) WHERE ( root_cid IS NULL AND meta->>'stable_key' = 'true' );


CREATE TABLE IF NOT EXISTS discover.manifests (
  manifest_id TEXT NOT NULL UNIQUE,
  drive_id TEXT,
  validated_at TIMESTAMP WITH TIME ZONE NOT NULL,
  uploaded_at TIMESTAMP WITH TIME ZONE NOT NULL
);
CREATE INDEX IF NOT EXISTS manifests_drive_id ON discover.manifests ( drive_id );

CREATE TABLE IF NOT EXISTS discover.manifest_entries (
  manifest_id TEXT NOT NULL REFERENCES discover.manifests ( manifest_id ),
  claimed_root_cid TEXT NOT NULL CONSTRAINT valid_root_cid CHECK ( discover.valid_cid_v1(claimed_root_cid) ),
  local_path TEXT NOT NULL,
  meta JSONB,
  CONSTRAINT singleton_path_record UNIQUE ( manifest_id, local_path ),
  CONSTRAINT singleton_cid_record UNIQUE ( claimed_root_cid, manifest_id )
);


CREATE TABLE IF NOT EXISTS discover.published_deals (
  deal_id BIGINT UNIQUE NOT NULL CONSTRAINT valid_id CHECK ( deal_id > 0 ),
  piece_cid TEXT NOT NULL REFERENCES discover.car_files ( piece_cid ),
  label_cid TEXT NOT NULL CONSTRAINT valid_label_cid CHECK ( discover.valid_cid_v1(label_cid) ),
  provider TEXT NOT NULL REFERENCES discover.providers ( provider ),
  client TEXT NOT NULL CONSTRAINT valid_client_id CHECK ( SUBSTRING( client FROM 1 FOR 2 ) IN ( 'f1', 'f3' ) ),
  fil_plus BOOL NOT NULL,
  status TEXT NOT NULL,
  status_meta TEXT,
  start_epoch INTEGER NOT NULL CONSTRAINT valid_start CHECK ( start_epoch > 0 ),
  start_time TIMESTAMP WITH TIME ZONE NOT NULL GENERATED ALWAYS AS ( TO_TIMESTAMP( start_epoch*30 + 1598306400 ) ) STORED,
  end_epoch INTEGER NOT NULL CONSTRAINT valid_end CHECK ( end_epoch > 0 ),
  end_time TIMESTAMP WITH TIME ZONE NOT NULL GENERATED ALWAYS AS ( TO_TIMESTAMP( end_epoch*30 + 1598306400 ) ) STORED,
  sector_start_epoch INTEGER CONSTRAINT valid_sector_start CHECK ( sector_start_epoch > 0 ),
  sector_start_time TIMESTAMP WITH TIME ZONE GENERATED ALWAYS AS ( TO_TIMESTAMP( sector_start_epoch*30 + 1598306400 ) ) STORED,
  entry_created TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
  entry_last_updated TIMESTAMP WITH TIME ZONE NOT NULL
);
CREATE INDEX IF NOT EXISTS published_deals_piece_cid ON discover.published_deals ( piece_cid );
CREATE INDEX IF NOT EXISTS published_deals_client ON discover.published_deals ( client );
CREATE INDEX IF NOT EXISTS published_deals_provider ON discover.published_deals ( provider );
CREATE INDEX IF NOT EXISTS published_deals_status ON discover.published_deals ( status );
CREATE TRIGGER trigger_deal_insert
  BEFORE INSERT ON discover.published_deals
  FOR EACH ROW
  EXECUTE PROCEDURE discover.update_entry_timestamp()
;
CREATE TRIGGER trigger_deal_updated
  BEFORE UPDATE ON discover.published_deals
  FOR EACH ROW
  WHEN (
    OLD.status IS DISTINCT FROM NEW.status
      OR
    OLD.status_meta IS DISTINCT FROM NEW.status_meta
      OR
    OLD.sector_start_epoch IS DISTINCT FROM NEW.sector_start_epoch
  )
  EXECUTE PROCEDURE discover.update_entry_timestamp()
;
CREATE TRIGGER trigger_basic_deal_history_on_insert
  AFTER INSERT ON discover.published_deals
  FOR EACH ROW
  EXECUTE PROCEDURE discover.record_deal_event()
;
CREATE TRIGGER trigger_basic_deal_history_on_update
  AFTER UPDATE ON discover.published_deals
  FOR EACH ROW
  WHEN (OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE PROCEDURE discover.record_deal_event()
;

CREATE TABLE IF NOT EXISTS discover.published_deals_events (
  entry_id BIGSERIAL UNIQUE NOT NULL,
  deal_id BIGINT NOT NULL REFERENCES discover.published_deals( deal_id ),
  status TEXT NOT NULL,
  entry_created TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS published_deals_events_deal_id ON discover.published_deals_events ( deal_id );
