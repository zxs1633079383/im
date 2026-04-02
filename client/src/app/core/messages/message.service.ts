import { Injectable, signal, inject } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';
import { filter, debounceTime } from 'rxjs/operators';
import { WebSocketService } from '../ws/websocket.service';
import { PushMsgPayload, PongPayload, SyncChannelState, SyncResponse } from '../ws/websocket.models';
import { ChannelService } from '../channels/channel.service';

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
  private channelService = inject(ChannelService);

  constructor(private http: HttpClient) {
    // Append pushed messages from the WebSocket to the active channel view.
    this.ws.pushMsg$.subscribe(msg => this.handlePush(msg));

    // When the server reports channels with a higher seq via pong,
    // pull any missed messages for those channels.
    this.ws.pong$.subscribe(pong => this.handlePong(pong));

    // On WebSocket connect (or reconnect), sync all known channels to catch
    // any messages missed while the connection was down.
    this.ws.connected$.pipe(
      filter(connected => connected),
      debounceTime(200),
    ).subscribe(() => this.batchSync());
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

  /**
   * Called when the user scrolls up past the oldest message currently displayed.
   * Uses count-based hole detection against the in-memory message list, then fetches
   * from server when a gap is found or there are not enough local messages.
   *
   * @param channelId  The channel being viewed.
   * @param pivotSeq   The seq of the oldest currently-displayed message.
   * @param pageSize   How many older messages to load (default 30).
   * @returns          The older messages to prepend (empty if already at the beginning).
   */
  async detectAndFillHole(channelId: number, pivotSeq: number, pageSize = 30): Promise<Message[]> {
    // 1. Check local in-memory messages for messages older than pivotSeq.
    const localMsgs = this.messages()
      .filter(m => m.seq > 0 && m.seq < pivotSeq)
      .sort((a, b) => a.seq - b.seq);

    // Take the most recent `pageSize` of those (closest to pivotSeq).
    const localPage = localMsgs.slice(-pageSize);
    const localSeqs = localPage.map(m => m.seq);

    // 2. Check continuity: are there gaps in the local sequence?
    const hasGap = this.hasSequenceGap(localSeqs, pivotSeq);

    // 3. If we have enough continuous messages locally, return them directly.
    if (!hasGap && localPage.length >= pageSize) {
      return localPage;
    }

    // 4. Gap detected (or not enough messages): fetch from server.
    try {
      const serverMsgs = await this.fetchMessages(channelId, {
        before_seq: pivotSeq,
        limit: pageSize,
      });

      // fetchMessages returns newest-first; reverse to ascending order.
      return [...serverMsgs].reverse();
    } catch (err) {
      console.warn('[MessageService] hole fill fetch failed', err);
      // Return whatever we have locally as a best-effort fallback.
      return localPage;
    }
  }

  /**
   * Returns true if there is a gap in `ascSeqs` (ascending) before `pivotSeq`.
   * A gap exists when consecutive seqs are not adjacent (seq[i+1] !== seq[i] + 1)
   * OR when the highest local seq does not connect directly to pivotSeq.
   */
  private hasSequenceGap(ascSeqs: number[], pivotSeq: number): boolean {
    if (ascSeqs.length === 0) return true;

    // Check continuity between consecutive elements.
    for (let i = 1; i < ascSeqs.length; i++) {
      if (ascSeqs[i] !== ascSeqs[i - 1] + 1) {
        return true; // non-contiguous seq detected
      }
    }

    // Check that the last local seq connects to pivotSeq without a gap.
    const highestLocal = ascSeqs[ascSeqs.length - 1];
    if (pivotSeq - highestLocal > 1) {
      return true; // gap between local data and pivot
    }

    return false;
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
   * On reconnect: POST /api/sync with all known channel states.
   * Server returns incremental messages for channels with small gaps,
   * has_more flag for large gaps, and any new channels joined while offline.
   *
   * This replaces the old per-channel fetchAndAppendMissed loop.
   */
  private async batchSync(): Promise<void> {
    // Build the channel state list from our WS seq tracker.
    const channels: SyncChannelState[] = Object.entries(this.ws.channelSeqs).map(
      ([idStr, seq]) => ({ id: Number(idStr), seq })
    );

    try {
      const resp = await firstValueFrom(
        this.http.post<SyncResponse>(`${API_BASE}/sync`, { channels })
      );

      for (const result of resp.channels ?? []) {
        // Update the WS seq tracker so heartbeat pong diffs stay accurate.
        this.ws.updateChannelSeq(result.id, result.server_seq);

        // If this is the active channel and we got messages, merge them in.
        if (result.messages && result.messages.length > 0) {
          if (this.activeChannelId() === result.id) {
            const existingSeqs = new Set(this.messages().map(m => m.seq));
            const newMsgs = result.messages
              .filter(m => !existingSeqs.has(m.seq))
              .map(m => m as unknown as Message);
            if (newMsgs.length > 0) {
              this.messages.update(existing =>
                [...existing, ...newMsgs].sort((a, b) => a.seq - b.seq)
              );
            }
          }
        }

        // Update channel unread counts via ChannelService.
        this.channelService.updateUnread(result.id, result.unread);
      }
    } catch (err) {
      console.warn('[MessageService] batchSync failed, falling back to individual pulls', err);
      // Fallback: old behavior for resilience.
      await this.syncAllChannelsFallback();
    }
  }

  /** Legacy per-channel sync; used as fallback when POST /api/sync fails. */
  private async syncAllChannelsFallback(): Promise<void> {
    const channels = this.channelService.channels();
    for (const ch of channels) {
      const localSeq = this.ws.channelSeqs[String(ch.id)] ?? -1;
      // ch.seq is the server's latest seq for this channel.
      if (localSeq >= 0 && ch.seq > localSeq) {
        await this.fetchAndAppendMissed(ch.id, localSeq);
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
