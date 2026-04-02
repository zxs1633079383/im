import { Injectable, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

// ---------- types ----------

export interface Message {
  id: number;
  channel_id: number;
  seq: number;
  client_msg_id?: string;
  sender_id: number;
  msg_type: number;   // 1=text, 2=image, 3=file, 4=system, 99=phantom
  content: string;
  visible_to?: number[];
  reply_to?: number;
  forwarded_from?: number;
  created_at: string;
}

export interface SendMessagePayload {
  content: string;
  client_msg_id?: string;
  msg_type?: number;
  visible_to?: number[];
  reply_to?: number;
}

export interface FetchOptions {
  after_seq?: number;
  before_seq?: number;
  around_seq?: number;
  limit?: number;
}

export interface FetchMessagesResponse {
  messages: Message[];
}

const API_BASE = 'http://localhost:8080/api';

@Injectable({ providedIn: 'root' })
export class MessageService {
  /** Messages for the currently-open channel, newest last. */
  readonly messages = signal<Message[]>([]);

  /** The channel ID whose messages are currently loaded. */
  readonly activeChannelId = signal<number | null>(null);

  constructor(private http: HttpClient) {}

  // ---------- API calls ----------

  async sendMessage(channelId: number, payload: SendMessagePayload): Promise<Message> {
    return firstValueFrom(
      this.http.post<Message>(`${API_BASE}/channels/${channelId}/messages`, payload)
    );
  }

  async fetchMessages(channelId: number, opts: FetchOptions = {}): Promise<Message[]> {
    const params: Record<string, string> = {};
    if (opts.after_seq !== undefined) params['after_seq'] = String(opts.after_seq);
    if (opts.before_seq !== undefined) params['before_seq'] = String(opts.before_seq);
    if (opts.around_seq !== undefined) params['around_seq'] = String(opts.around_seq);
    if (opts.limit !== undefined) params['limit'] = String(opts.limit);

    const resp = await firstValueFrom(
      this.http.get<FetchMessagesResponse>(`${API_BASE}/channels/${channelId}/messages`, {
        params,
      })
    );
    return resp.messages ?? [];
  }

  async markRead(channelId: number): Promise<void> {
    await firstValueFrom(
      this.http.post(`${API_BASE}/channels/${channelId}/read`, {})
    );
  }

  // ---------- state management ----------

  /** Load the latest 50 messages for a channel and update the messages signal. */
  async loadMessages(channelId: number): Promise<void> {
    const msgs = await this.fetchMessages(channelId, { limit: 50 });
    // FetchBefore (default) returns newest-first; reverse for display order.
    this.messages.set([...msgs].reverse());
    this.activeChannelId.set(channelId);
  }

  /** Append a locally-sent message optimistically (before ACK). */
  appendOptimistic(msg: Message): void {
    this.messages.update(msgs => [...msgs, msg]);
  }

  /** Replace an optimistic message (matched by client_msg_id) with the ACK'd version. */
  confirmSent(clientMsgId: string, confirmed: Message): void {
    this.messages.update(msgs =>
      msgs.map(m => (m.client_msg_id === clientMsgId ? confirmed : m))
    );
  }

  /** Remove an optimistic message (e.g. on send failure) by client_msg_id. */
  removeOptimistic(clientMsgId: string): void {
    this.messages.update(msgs => msgs.filter(m => m.client_msg_id !== clientMsgId));
  }

  /** Clear messages when navigating away from a channel. */
  clear(): void {
    this.messages.set([]);
    this.activeChannelId.set(null);
  }
}
