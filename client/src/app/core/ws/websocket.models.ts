export type WSMessageType =
  | 'ping' | 'pong'
  | 'push_msg' | 'push_ack'
  | 'send' | 'send_ack'
  | 'sync' | 'sync_resp'
  | 'read_sync'
  | 'friend_event';

export interface WSFrame<T = unknown> {
  type: WSMessageType;
  payload: T;
}

export interface PingPayload {
  channel_seqs: Record<string, number>; // channel_id (string) → local max seq
}

export interface PongPayload {
  server_time: number;
  channel_seqs: Record<string, number>; // only channels with diff
}

export interface PushMsgPayload {
  push_id: string;
  channel_id: number;
  seq: number;
  server_msg_id: number;
  sender_id: number;
  content: string;
  msg_type: number; // 1=normal, 2=phantom
  visible_to?: number[];
  created_at: string;
}

export interface PushACKPayload {
  push_id: string;
}

export interface SendPayload {
  client_msg_id: string;
  channel_id: number;
  content: string;
  msg_type?: number;
  visible_to?: number[];
}

export interface SendACKPayload {
  client_msg_id: string;
  server_msg_id: number;
  seq: number;
  channel_id: number;
}

export interface SyncChannelState {
  id: number;
  seq: number;
}

export interface SyncPayload {
  channels: SyncChannelState[];
}

export interface ReadSyncPayload {
  channel_id: number;
  read_seq: number;
}

export interface FriendEventPayload {
  event_type: string; // 'request' | 'accepted' | 'rejected'
  from_user_id: number;
}

// Mirror of message.service.ts Message — kept here to avoid circular deps.
export interface SyncMessage {
  id: number;
  channel_id: number;
  seq: number;
  client_msg_id?: string;
  sender_id: number;
  msg_type: number;
  content: string;
  visible_to?: number[];
  created_at: string;
}

export interface SyncChannelResult {
  id: number;
  server_seq: number;
  unread: number;
  messages?: SyncMessage[];
  has_more: boolean;
}

export interface SyncResponse {
  channels: SyncChannelResult[];
}
