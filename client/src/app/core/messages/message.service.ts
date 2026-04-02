import { Injectable, signal, inject } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { WebSocketService } from '../ws/websocket.service';
import { PushMsgPayload, PongPayload } from '../ws/websocket.models';

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

  private ws = inject(WebSocketService);

  constructor(private http: HttpClient) {
    // Append pushed messages from the WebSocket to the active channel view.
    this.ws.pushMsg$.subscribe(msg => this.handlePush(msg));

    // When the server reports channels with a higher seq via pong,
    // pull any missed messages for those channels.
    this.ws.pong$.subscribe(pong => this.handlePong(pong));
  }

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

  // ---- WebSocket push handlers ----

  /**
   * Handle a push_msg from the WebSocket.
   * If the message belongs to the currently active channel, append it to
   * the in-memory list (deduplicating by seq to avoid showing it twice
   * when also received via HTTP).
   */
  private handlePush(pushed: PushMsgPayload): void {
    // Update the WS channel seq tracker.
    this.ws.updateChannelSeq(pushed.channel_id, pushed.seq);

    // Only append to the visible list if this channel is currently open.
    if (this.activeChannelId() !== pushed.channel_id) return;

    // Phantom messages (msg_type === 2) visible only to specific recipients —
    // skip them in the generic list; callers can subscribe to pushMsg$ directly.
    if (pushed.msg_type === 2) return;

    // Deduplicate: don't show if we already have this seq.
    const alreadyPresent = this.messages().some(m => m.seq === pushed.seq);
    if (alreadyPresent) return;

    const msg: Message = {
      id: pushed.server_msg_id,
      channel_id: pushed.channel_id,
      seq: pushed.seq,
      sender_id: pushed.sender_id,
      msg_type: pushed.msg_type,
      content: pushed.content,
      visible_to: pushed.visible_to,
      created_at: pushed.created_at,
    };
    this.messages.update(msgs => [...msgs, msg]);
  }

  /**
   * Handle a pong from the WebSocket.
   * For every channel the server reports as ahead of our local seq, pull
   * the missed messages using the existing HTTP endpoint.
   */
  private handlePong(pong: PongPayload): void {
    for (const [chIdStr, serverSeq] of Object.entries(pong.channel_seqs ?? {})) {
      const chId = Number(chIdStr);
      const localSeq = this.ws.channelSeqs[chIdStr] ?? 0;
      if (serverSeq > localSeq) {
        this.fetchAndAppendMissed(chId, localSeq);
      }
    }
  }

  /**
   * Pull messages from the server that arrived after `afterSeq` and
   * append them to the active channel view if appropriate.
   */
  private async fetchAndAppendMissed(channelId: number, afterSeq: number): Promise<void> {
    try {
      const msgs = await this.fetchMessages(channelId, { after_seq: afterSeq, limit: 100 });
      for (const msg of msgs) {
        this.ws.updateChannelSeq(channelId, msg.seq);
      }
      // If this is the active channel, merge into the visible list.
      if (this.activeChannelId() === channelId && msgs.length > 0) {
        const existingSeqs = new Set(this.messages().map(m => m.seq));
        const newMsgs = msgs.filter(m => !existingSeqs.has(m.seq));
        if (newMsgs.length > 0) {
          this.messages.update(existing => [...existing, ...newMsgs].sort((a, b) => a.seq - b.seq));
        }
      }
    } catch (err) {
      console.warn('[MessageService] fetchAndAppendMissed failed', channelId, err);
    }
  }
}
