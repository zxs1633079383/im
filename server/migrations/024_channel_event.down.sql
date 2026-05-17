-- C017 / C018 / C019 rollback
-- 注意：sequence 是动态 CREATE，down 时无法枚举全部 channel_*_seq_* sequence；
--       生产 down 必须配套 `make migrate-channel-event-drop-sequences` 脚本扫所有 channel_msg_seq_* / channel_event_seq_* 后 DROP

DROP TABLE IF EXISTS channel_event_p00;
DROP TABLE IF EXISTS channel_event_p01;
DROP TABLE IF EXISTS channel_event_p02;
DROP TABLE IF EXISTS channel_event_p03;
DROP TABLE IF EXISTS channel_event_p04;
DROP TABLE IF EXISTS channel_event_p05;
DROP TABLE IF EXISTS channel_event_p06;
DROP TABLE IF EXISTS channel_event_p07;
DROP TABLE IF EXISTS channel_event_p08;
DROP TABLE IF EXISTS channel_event_p09;
DROP TABLE IF EXISTS channel_event_p10;
DROP TABLE IF EXISTS channel_event_p11;
DROP TABLE IF EXISTS channel_event_p12;
DROP TABLE IF EXISTS channel_event_p13;
DROP TABLE IF EXISTS channel_event_p14;
DROP TABLE IF EXISTS channel_event_p15;
DROP TABLE IF EXISTS channel_event;
DROP TABLE IF EXISTS channel_sequence_meta;