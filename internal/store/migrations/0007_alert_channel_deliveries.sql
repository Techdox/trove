-- Track successful instant-alert deliveries per incident and channel. A
-- temporary row exists only while another configured channel still needs a
-- retry; it is removed once the entire fan-out succeeds.
CREATE TABLE alert_channel_deliveries (
    delivery_key TEXT NOT NULL,
    channel      TEXT NOT NULL,
    delivered_at INTEGER NOT NULL,
    PRIMARY KEY (delivery_key, channel)
);
