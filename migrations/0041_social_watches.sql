CREATE TABLE mantik_social_watch_journal (
 singleton boolean PRIMARY KEY CHECK (singleton),
 payload bytea NOT NULL CHECK (octet_length(payload) <= 8388608)
);
